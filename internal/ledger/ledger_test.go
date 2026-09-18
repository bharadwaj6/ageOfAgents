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
	"runtime"
	"slices"
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

// A reader must not need write access. The status, events and diagnose
// commands only read the log, and a log copied somewhere read-only (a CI
// artifact, another user's workspace) has no lock file and cannot get one.
func TestOpenReadsAReadOnlyLog(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not stop file creation on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permission bits")
	}
	src := filepath.Join(t.TempDir(), "events.jsonl")
	w, err := Open(src)
	if err != nil {
		t.Fatalf("Open writer: %v", err)
	}
	mustAppend(t, w, mustEvent(t, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "x"}))
	mustAppend(t, w, mustEvent(t, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g2", Text: "y"}))
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read source log: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("copy log: %v", err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("make dir read-only: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Errorf("restore dir: %v", err)
		}
	})

	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open of a read-only log: %v", err)
	}
	events, err := l.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if _, err := l.Append(mustEvent(t, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g3", Text: "z"})); err == nil {
		t.Fatal("Append succeeded without being able to take the lock")
	}
}

// Update's decide runs under the cross-process lock: another handle's Append
// must wait for it, and land after the events decide chose to append.
func TestUpdateBlocksOtherHandlesAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	a, err := Open(path)
	if err != nil {
		t.Fatalf("Open a: %v", err)
	}
	b, err := Open(path)
	if err != nil {
		t.Fatalf("Open b: %v", err)
	}
	mustAppend(t, a, mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))

	entered, gate := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release) // never leave decide blocked, even on failure
	type result struct {
		stored []api.Event
		err    error
	}
	updated := make(chan result, 1)
	go func() {
		stored, err := a.Update(func(events []api.Event) ([]api.Event, error) {
			close(entered)
			<-gate
			// A decision that depends on what was read: it is only correct if
			// nothing lands between the read and the write.
			return []api.Event{mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: strconv.Itoa(len(events))})}, nil
		})
		updated <- result{stored, err}
	}()
	<-entered

	appended := make(chan result, 1)
	go func() {
		e, err := b.Append(mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "b"}))
		appended <- result{[]api.Event{e}, err}
	}()
	select {
	case got := <-appended:
		t.Fatalf("another handle appended seq %d while decide held the log", got.stored[0].Seq)
	case <-time.After(100 * time.Millisecond):
	}
	release()

	u := <-updated
	if u.err != nil {
		t.Fatalf("Update: %v", u.err)
	}
	if len(u.stored) != 1 || u.stored[0].Seq != 2 {
		t.Fatalf("Update stored %+v, want one event at seq 2", u.stored)
	}
	var app result
	select {
	case app = <-appended:
	case <-time.After(30 * time.Second):
		t.Fatal("Append still blocked after Update finished")
	}
	if app.err != nil {
		t.Fatalf("Append: %v", app.err)
	}
	if app.stored[0].Seq != 3 {
		t.Errorf("Append seq = %d, want 3 (after the Update's event)", app.stored[0].Seq)
	}
	requireGapless(t, path, 3)
}

// Many handles mixing Update and Append: every Update must append directly
// after the log it read — the read-decide-append is one step, never split by
// another writer.
func TestConcurrentUpdatesSeeTheLogTheyExtend(t *testing.T) {
	const updaters, appenders, each = 4, 2, 25
	path := filepath.Join(t.TempDir(), "events.jsonl")
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < updaters+appenders; i++ {
		l, err := Open(path)
		if err != nil {
			t.Fatalf("Open %d: %v", i, err)
		}
		wg.Add(1)
		go func(update bool) {
			defer wg.Done()
			<-start
			for j := 0; j < each; j++ {
				if !update {
					if _, err := l.Append(mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "append"})); err != nil {
						t.Errorf("Append: %v", err)
						return
					}
					continue
				}
				var seen int
				stored, err := l.Update(func(events []api.Event) ([]api.Event, error) {
					seen = len(events)
					return []api.Event{mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "update"})}, nil
				})
				if err != nil {
					t.Errorf("Update: %v", err)
					return
				}
				if len(stored) != 1 || stored[0].Seq != seen+1 {
					t.Errorf("Update read %d events but stored %+v; want seq %d", seen, stored, seen+1)
					return
				}
			}
		}(i < updaters)
	}
	close(start)
	wg.Wait()
	requireGapless(t, path, (updaters+appenders)*each)
}

func TestUpdateAppendsWhatDecideReturns(t *testing.T) {
	sentinel := errors.New("no")
	tests := []struct {
		name      string
		decide    func(t *testing.T, events []api.Event) ([]api.Event, error)
		wantErr   error
		wantSeqs  []int // seqs Update reports storing (and the hook sees)
		wantTotal int   // events on the log afterwards
	}{
		{
			name: "an error appends nothing",
			decide: func(t *testing.T, _ []api.Event) ([]api.Event, error) {
				return []api.Event{mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "x"})}, sentinel
			},
			wantErr:   sentinel,
			wantTotal: 2,
		},
		{
			name:      "nothing to append",
			decide:    func(*testing.T, []api.Event) ([]api.Event, error) { return nil, nil },
			wantTotal: 2,
		},
		{
			name: "several events take consecutive seqs",
			decide: func(t *testing.T, _ []api.Event) ([]api.Event, error) {
				return []api.Event{
					mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "x"}),
					mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "y"}),
				}, nil
			},
			wantSeqs:  []int{3, 4},
			wantTotal: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			l, err := Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			mustAppend(t, l, mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
			mustAppend(t, l, mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
			var hooked []int
			l.SetAppendHook(func(e api.Event) { hooked = append(hooked, e.Seq) })

			var seen []api.Event
			stored, err := l.Update(func(events []api.Event) ([]api.Event, error) {
				seen = events
				return tt.decide(t, events)
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Update error = %v, want %v", err, tt.wantErr)
			}
			if len(seen) != 2 || seen[0].Seq != 1 || seen[1].Seq != 2 {
				t.Errorf("decide saw %+v, want the two events on the log", seen)
			}
			var got []int
			for _, e := range stored {
				got = append(got, e.Seq)
			}
			if !slices.Equal(got, tt.wantSeqs) {
				t.Errorf("stored seqs = %v, want %v", got, tt.wantSeqs)
			}
			if !slices.Equal(hooked, tt.wantSeqs) {
				t.Errorf("hook saw seqs %v, want %v", hooked, tt.wantSeqs)
			}
			requireGapless(t, path, tt.wantTotal)
			// Whatever Update did, the next Append carries on from the log.
			next, err := l.Append(mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
			if err != nil {
				t.Fatalf("Append after Update: %v", err)
			}
			if next.Seq != tt.wantTotal+1 {
				t.Errorf("next Append seq = %d, want %d", next.Seq, tt.wantTotal+1)
			}
		})
	}
}

// --- Reading from an offset -------------------------------------------------
//
// ReadFrom is how a follower tails the log while writers append. It takes no
// lock, so it can see a writer mid-line: it must hand back only whole lines and
// leave the rest, untouched, for its next call.

// fileLen returns the size of the file at path.
func fileLen(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return fi.Size()
}

// appendRaw writes b to the end of the file at path, bypassing the Ledger, as
// another writer part-way through a line would.
func appendRaw(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for raw write: %v", err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatalf("raw write: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close after raw write: %v", err)
	}
}

func TestReadFromReturnsOnlyCompleteLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	l, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	mustAppend(t, l, mustEvent(t, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "x"}))
	mustAppend(t, l, mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
	whole, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	// Half of seq 3, as another writer leaves it between its first and last byte.
	e := mustEvent(t, api.Merged, api.MergedPayload{TicketID: "t1", Worker: "a", Commit: "abc"})
	e.Seq = 3
	line, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	half := len(line) / 2
	appendRaw(t, path, line[:half])
	torn := fileLen(t, path)

	lines, next, err := l.ReadFrom(0)
	if err != nil {
		t.Fatalf("ReadFrom(0): %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want the 2 complete ones", len(lines))
	}
	if next != int64(len(whole)) {
		t.Errorf("next = %d, want %d: just past the last complete line", next, len(whole))
	}
	wantBytes := bytes.Split(bytes.TrimSuffix(whole, []byte("\n")), []byte("\n"))
	for i, got := range lines {
		if got.Event.Seq != i+1 {
			t.Errorf("line %d: seq = %d, want %d", i, got.Event.Seq, i+1)
		}
		if !bytes.Equal(got.Bytes, wantBytes[i]) {
			t.Errorf("line %d: Bytes = %q, want the line exactly as stored: %q", i, got.Bytes, wantBytes[i])
		}
	}
	if size := fileLen(t, path); size != torn {
		t.Fatalf("ReadFrom changed the log from %d to %d bytes; a reader must never truncate", torn, size)
	}

	// Nothing new is complete yet: an empty read that keeps its place.
	lines, again, err := l.ReadFrom(next)
	if err != nil {
		t.Fatalf("ReadFrom(next) mid-line: %v", err)
	}
	if len(lines) != 0 || again != next {
		t.Errorf("mid-line read = %d lines, next %d; want 0 lines, next %d", len(lines), again, next)
	}

	// The writer finishes; the line is now whole and is returned exactly once.
	appendRaw(t, path, append(line[half:], '\n'))
	lines, next, err = l.ReadFrom(next)
	if err != nil {
		t.Fatalf("ReadFrom after the line completed: %v", err)
	}
	if len(lines) != 1 || lines[0].Event.Seq != 3 || lines[0].Event.Type != api.Merged {
		t.Fatalf("got %+v, want the single completed seq 3", lines)
	}
	if !bytes.Equal(lines[0].Bytes, line) {
		t.Errorf("Bytes = %q, want %q", lines[0].Bytes, line)
	}
	if size := fileLen(t, path); next != size {
		t.Errorf("next = %d, want the end of the log, %d", next, size)
	}
}

// A follower's offset is only meaningful for the log it was read from. If the
// log was truncated or replaced since, ReadFrom must say so rather than read
// from a position that is now the middle of someone else's line.
func TestReadFromDetectsShrink(t *testing.T) {
	tests := []struct {
		name    string
		replace func(t *testing.T, path string)
	}{
		{
			name: "truncated to its first line",
			replace: func(t *testing.T, path string) {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read: %v", err)
				}
				if err := os.WriteFile(path, data[:bytes.IndexByte(data, '\n')+1], 0o644); err != nil {
					t.Fatalf("rewrite: %v", err)
				}
			},
		},
		{
			name: "removed",
			replace: func(t *testing.T, path string) {
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove: %v", err)
				}
			},
		},
		{
			// Shrank, then grew back past the old offset before the next read:
			// the offset now falls inside a line.
			name: "replaced by one longer line",
			replace: func(t *testing.T, path string) {
				e := mustEvent(t, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: strings.Repeat("x", 512)})
				e.Seq = 1
				line, err := json.Marshal(e)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
					t.Fatalf("rewrite: %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			l, err := Open(path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			for i := 0; i < 3; i++ {
				mustAppend(t, l, mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
			}
			_, next, err := l.ReadFrom(0)
			if err != nil {
				t.Fatalf("ReadFrom(0): %v", err)
			}
			tt.replace(t, path)

			lines, _, err := l.ReadFrom(next)
			if !errors.Is(err, ErrLogShrank) {
				t.Fatalf("ReadFrom(%d) = %d lines, err %v; want ErrLogShrank", next, len(lines), err)
			}
			// Starting over is always possible.
			if _, _, err := l.ReadFrom(0); err != nil {
				t.Errorf("ReadFrom(0) after the shrink: %v", err)
			}
		})
	}
}
