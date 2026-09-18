package ledger

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/filelock"
	"github.com/bharadwaj6/ageOfAgents/internal/invariant"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

func mustEvent(t *testing.T, typ api.EventType, payload any) api.Event {
	t.Helper()
	e, err := api.NewEvent(typ, "test", payload)
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	return e
}

func mustAppend(t *testing.T, l *Ledger, e api.Event) {
	t.Helper()
	if _, err := l.Append(e); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

func TestAppendAssignsMonotonicSeq(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for i := 1; i <= 3; i++ {
		stored, err := l.Append(mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if stored.Seq != i {
			t.Errorf("append %d: seq = %d, want %d", i, stored.Seq, i)
		}
	}
}

func TestReadReturnsEventsInOrder(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustAppend(t, l, mustEvent(t, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "do it"}))
	mustAppend(t, l, mustEvent(t, api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "impl", IdempotencyKey: "k1"}))

	events, err := l.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Type != api.GoalSubmitted || events[1].Type != api.TicketCreated {
		t.Errorf("unexpected order: %v, %v", events[0].Type, events[1].Type)
	}
	var p api.TicketCreatedPayload
	if err := events[1].DecodePayload(&p); err != nil {
		t.Fatalf("DecodePayload: %v", err)
	}
	if p.TicketID != "t1" || p.IdempotencyKey != "k1" {
		t.Errorf("payload round-trip wrong: %+v", p)
	}
}

func TestReopenResumesSequence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l1, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustAppend(t, l1, mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
	mustAppend(t, l1, mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))

	l2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	stored, err := l2.Append(mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "b"}))
	if err != nil {
		t.Fatalf("Append after reopen: %v", err)
	}
	if stored.Seq != 3 {
		t.Errorf("seq after reopen = %d, want 3", stored.Seq)
	}
}

func TestReadMissingFileIsEmpty(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "nested", "events.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	events, err := l.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("expected empty, got %d", len(events))
	}
}

func TestReplayStopsOnError(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustAppend(t, l, mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
	mustAppend(t, l, mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "b"}))

	count := 0
	sentinel := errSentinel{}
	err = l.Replay(func(api.Event) error {
		count++
		return sentinel
	})
	if err != sentinel {
		t.Errorf("expected sentinel error, got %v", err)
	}
	if count != 1 {
		t.Errorf("replay should stop after first error, ran %d times", count)
	}
}

type errSentinel struct{}

func (errSentinel) Error() string { return "sentinel" }

func TestConcurrentAppendsUniqueSeq(t *testing.T) {
	l, err := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			mustAppend(t, l, mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
		}()
	}
	wg.Wait()

	events, err := l.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != n {
		t.Fatalf("got %d events, want %d", len(events), n)
	}
	seen := make(map[int]bool, n)
	for _, e := range events {
		if seen[e.Seq] {
			t.Errorf("duplicate seq %d", e.Seq)
		}
		seen[e.Seq] = true
	}
}

// TestConcurrentAppendsGaplessAndParseable stresses Append under heavy
// concurrency and asserts the AGENTS.md invariant: every line stays a complete,
// parseable JSON event and sequence numbers are gapless 1..N with no corruption
// from interleaved writes.
func TestConcurrentAppendsGaplessAndParseable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	const n = 300
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			mustAppend(t, l, mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "w", TicketID: "t"}))
		}(i)
	}
	wg.Wait()

	// Every persisted line must be a complete, parseable event (no torn or
	// interleaved bytes), and the seq set must be exactly {1..n}.
	events, _, err := scan(path)
	if err != nil {
		t.Fatalf("scan after concurrent appends: %v", err)
	}
	if len(events) != n {
		t.Fatalf("got %d events, want %d", len(events), n)
	}
	seqs := make([]bool, n+1)
	for _, e := range events {
		if e.Seq < 1 || e.Seq > n || seqs[e.Seq] {
			t.Fatalf("bad or duplicate seq %d", e.Seq)
		}
		seqs[e.Seq] = true
	}
	for s := 1; s <= n; s++ {
		if !seqs[s] {
			t.Errorf("missing seq %d (gap)", s)
		}
	}
}

// TestReadToleratesTornTrailingLine simulates a crash mid-Append: the last line
// is partially written (no newline, truncated JSON). Read must skip it, and
// reopening must repair the file so the next Append produces a clean log.
func TestReadToleratesTornTrailingLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustAppend(t, l, mustEvent(t, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "x"}))
	mustAppend(t, l, mustEvent(t, api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "impl", IdempotencyKey: "k"}))

	// Append a torn line, as a crash would leave it.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for torn write: %v", err)
	}
	if _, err := f.WriteString(`{"seq":3,"type":"Merged","payl`); err != nil {
		t.Fatalf("torn write: %v", err)
	}
	_ = f.Close()

	// Read tolerates the torn tail.
	events, err := l.Read()
	if err != nil {
		t.Fatalf("Read with torn tail: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (torn line skipped)", len(events))
	}

	// Reopen repairs the tail; the next append is seq 3 and the log is clean.
	l2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after torn tail: %v", err)
	}
	stored, err := l2.Append(mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "w"}))
	if err != nil {
		t.Fatalf("append after repair: %v", err)
	}
	if stored.Seq != 3 {
		t.Errorf("seq after repair = %d, want 3", stored.Seq)
	}
	events, err = l2.Read()
	if err != nil {
		t.Fatalf("Read after repair: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events after repair, want 3", len(events))
	}
}

// TestScanErrorsOnMidLogCorruption ensures non-tail corruption is surfaced (not
// silently truncated): a garbage line followed by further lines is an error.
func TestScanErrorsOnMidLogCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	contents := `{"seq":1,"type":"Heartbeat"}` + "\n" +
		"this is not json" + "\n" +
		`{"seq":3,"type":"Heartbeat"}` + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, err := scan(path); err == nil {
		t.Error("expected error for mid-log corruption, got nil")
	}
}

// --- Several writers on one log -------------------------------------------
//
// aoa goal/approve/amend append to the log while aoa run or serve is live, each
// through its own Ledger. These tests hold every writer to the invariant the
// replay checks: seqs are exactly 1..N in file order.

// sidecarLock is the path writers lock. It is a contract between aoa processes,
// possibly of different versions, so the test spells it out rather than
// borrowing the implementation's constant.
func sidecarLock(path string) string { return path + ".lock" }

// requireGapless fails the test unless the log at path holds exactly n events
// numbered 1..n in file order.
func requireGapless(t *testing.T, path string, n int) {
	t.Helper()
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open for check: %v", err)
	}
	events, err := l.Read()
	if err != nil {
		t.Fatalf("Read for check: %v", err)
	}
	seqs := make([]int, len(events))
	for i, e := range events {
		seqs[i] = e.Seq
	}
	if len(events) != n {
		t.Fatalf("got %d events, want %d; seqs %v", len(events), n, seqs)
	}
	if vs := invariant.MonotonicGaplessSeq(events); len(vs) > 0 {
		t.Fatalf("seqs %v are not 1..%d: %d violations, first: %s", seqs, n, len(vs), vs[0].Detail)
	}
}

func TestTwoHandlesShareOneSequence(t *testing.T) {
	tests := []struct {
		name  string
		order string // which handle appends, in turn
	}{
		{name: "alternating", order: "aba"},
		{name: "second handle runs ahead", order: "abbba"},
		{name: "first handle idles", order: "abbbb"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			a, err := Open(path)
			if err != nil {
				t.Fatalf("Open a: %v", err)
			}
			b, err := Open(path)
			if err != nil {
				t.Fatalf("Open b: %v", err)
			}
			handles := map[rune]*Ledger{'a': a, 'b': b}
			for i, h := range tt.order {
				stored, err := handles[h].Append(mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: string(h)}))
				if err != nil {
					t.Fatalf("append %d via %c: %v", i+1, h, err)
				}
				if stored.Seq != i+1 {
					t.Errorf("append %d via %c returned seq %d, want %d", i+1, h, stored.Seq, i+1)
				}
			}
			requireGapless(t, path, len(tt.order))
		})
	}
}

// A handle must notice what happened to the file since its last write, not
// trust the seq it cached.
func TestAppendResyncsWithChangesOnDisk(t *testing.T) {
	tests := []struct {
		name    string
		before  int                     // appends through the handle first
		disturb func(path string) error // what happens behind its back
		wantSeq int                     // seq of the handle's next append
	}{
		{
			name:   "crashed writer left a torn tail",
			before: 2,
			disturb: func(path string) error {
				f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
				if err != nil {
					return err
				}
				if _, err := f.WriteString(`{"seq":3,"type":"Merged","payl`); err != nil {
					return errors.Join(err, f.Close())
				}
				return f.Close()
			},
			wantSeq: 3,
		},
		{
			name:   "log rewritten shorter",
			before: 3,
			disturb: func(path string) error {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				first := data[:bytes.IndexByte(data, '\n')+1]
				return os.WriteFile(path, first, 0o644)
			},
			wantSeq: 2,
		},
		{
			// The handle's byte offset now falls mid-line. Parsing from there
			// would mistake the rest of that line for a torn tail and cut it.
			name:   "log rewritten as one longer line",
			before: 2,
			disturb: func(path string) error {
				e, err := api.NewEvent(api.GoalSubmitted, "test", api.GoalSubmittedPayload{GoalID: "g1", Text: strings.Repeat("x", 512)})
				if err != nil {
					return err
				}
				e.Seq = 1
				line, err := json.Marshal(e)
				if err != nil {
					return err
				}
				return os.WriteFile(path, append(line, '\n'), 0o644)
			},
			wantSeq: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			l, err := Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			for i := 0; i < tt.before; i++ {
				mustAppend(t, l, mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
			}
			if err := tt.disturb(path); err != nil {
				t.Fatalf("disturb: %v", err)
			}
			stored, err := l.Append(mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
			if err != nil {
				t.Fatalf("Append after disturbance: %v", err)
			}
			if stored.Seq != tt.wantSeq {
				t.Errorf("seq = %d, want %d", stored.Seq, tt.wantSeq)
			}
			requireGapless(t, path, tt.wantSeq)
		})
	}
}

const (
	helperEnv     = "AOA_LEDGER_HELPER"
	helperPathEnv = "AOA_LEDGER_HELPER_PATH"
	helperNEnv    = "AOA_LEDGER_HELPER_N"
)

// TestLedgerHelperProcess is not a test. It is the child process that
// TestAppendsFromSeparateProcessesAreGapless starts: it opens the log, reports
// "ready", waits for its stdin to close and then appends.
func TestLedgerHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		return
	}
	if err := runLedgerHelper(); err != nil {
		fmt.Fprintln(os.Stderr, "ledger helper:", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runLedgerHelper() error {
	n, err := strconv.Atoi(os.Getenv(helperNEnv))
	if err != nil {
		return fmt.Errorf("parse %s: %w", helperNEnv, err)
	}
	l, err := Open(os.Getenv(helperPathEnv))
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	if _, err := fmt.Println("ready"); err != nil {
		return fmt.Errorf("report ready: %w", err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		return fmt.Errorf("wait for start: %w", err)
	}
	for i := 0; i < n; i++ {
		e, err := api.NewEvent(api.Heartbeat, "helper", api.HeartbeatPayload{Worker: strconv.Itoa(os.Getpid())})
		if err != nil {
			return fmt.Errorf("new event: %w", err)
		}
		if _, err := l.Append(e); err != nil {
			return fmt.Errorf("append %d: %w", i+1, err)
		}
	}
	return nil
}

// Real processes, as with aoa goal beside a live aoa run. Every child opens the
// log before any appends — a barrier — so each starts from the same stale view
// and all of them append at once.
func TestAppendsFromSeparateProcessesAreGapless(t *testing.T) {
	const procs, perProc = 4, 50
	path := filepath.Join(t.TempDir(), "events.jsonl")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	type child struct {
		cmd    *exec.Cmd
		start  io.WriteCloser
		stderr bytes.Buffer
	}
	children := make([]*child, procs)
	for i := range children {
		c := &child{cmd: exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLedgerHelperProcess$")}
		c.cmd.Env = append(os.Environ(),
			helperEnv+"=1", helperPathEnv+"="+path, helperNEnv+"="+strconv.Itoa(perProc))
		c.cmd.Stderr = &c.stderr
		var err error
		if c.start, err = c.cmd.StdinPipe(); err != nil {
			t.Fatalf("stdin pipe: %v", err)
		}
		stdout, err := c.cmd.StdoutPipe()
		if err != nil {
			t.Fatalf("stdout pipe: %v", err)
		}
		if err := c.cmd.Start(); err != nil {
			t.Fatalf("start helper %d: %v", i, err)
		}
		if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
			t.Fatalf("helper %d not ready: %q, %v; stderr: %s", i, line, err, c.stderr.String())
		}
		children[i] = c
	}
	for _, c := range children {
		if err := c.start.Close(); err != nil {
			t.Fatalf("release helper: %v", err)
		}
	}
	for i, c := range children {
		if err := c.cmd.Wait(); err != nil {
			t.Fatalf("helper %d: %v; stderr: %s", i, err, c.stderr.String())
		}
	}
	requireGapless(t, path, procs*perProc)
}

// Open repairs a torn tail by truncating it. While another writer holds the
// lock, a partial last line is that writer mid-append, not a crash: Open must
// wait for it rather than cut it off.
func TestOpenWaitsForInFlightAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	lock, err := os.OpenFile(sidecarLock(path), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	t.Cleanup(func() {
		if err := lock.Close(); err != nil {
			t.Errorf("close lock: %v", err)
		}
	})
	if err := filelock.Lock(lock); err != nil {
		t.Fatalf("take lock: %v", err)
	}

	// Write the first half of seq 1, as a writer does between its two writes.
	e := mustEvent(t, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "x"})
	e.Seq = 1
	line, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	line = append(line, '\n')
	half := len(line) / 2
	if err := os.WriteFile(path, line[:half], 0o644); err != nil {
		t.Fatalf("write first half: %v", err)
	}

	type opened struct {
		l   *Ledger
		err error
	}
	done := make(chan opened, 1)
	go func() {
		l, err := Open(path)
		done <- opened{l, err}
	}()
	select {
	case <-done:
		t.Fatal("Open returned while another writer held the lock mid-line")
	case <-time.After(100 * time.Millisecond):
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("reopen for second half: %v", err)
	}
	if _, err := f.Write(line[half:]); err != nil {
		t.Fatalf("write second half: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close after second half: %v", err)
	}
	if err := filelock.Unlock(lock); err != nil {
		t.Fatalf("release lock: %v", err)
	}

	var got opened
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Open still blocked after the lock was released")
	}
	if got.err != nil {
		t.Fatalf("Open: %v", got.err)
	}
	stored, err := got.l.Append(mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if stored.Seq != 2 {
		t.Errorf("seq after the in-flight event = %d, want 2", stored.Seq)
	}
	requireGapless(t, path, 2)
}
