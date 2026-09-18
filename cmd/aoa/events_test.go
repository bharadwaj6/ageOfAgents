package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/config"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// eventsWorkspace makes a workspace whose Event Log holds n events: odd seqs
// are GoalSubmitted, even seqs Heartbeat. It returns the workspace root and a
// handle for appending more.
func eventsWorkspace(t *testing.T, n int) (string, *ledger.Ledger) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte("backend = \"mock\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ws, err := workspaceAt(root)
	if err != nil {
		t.Fatalf("workspaceAt: %v", err)
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	appendEvents(t, led, n)
	return root, led
}

// appendEvents appends n events to led, continuing eventsWorkspace's pattern.
func appendEvents(t *testing.T, led *ledger.Ledger, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		typ, payload := api.Heartbeat, any(api.HeartbeatPayload{Worker: "w"})
		stored, err := led.Read()
		if err != nil {
			t.Fatalf("read ledger: %v", err)
		}
		if len(stored)%2 == 0 { // the next seq is odd
			typ, payload = api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g" + strconv.Itoa(len(stored)+1), Text: "x"}
		}
		e, err := api.NewEvent(typ, "test", payload)
		if err != nil {
			t.Fatalf("NewEvent: %v", err)
		}
		if _, err := led.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

// runEvents runs `aoa events` with args and returns what it printed.
func runEvents(t *testing.T, args ...string) string {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = cmdEvents(args) })
	if err != nil {
		t.Fatalf("aoa events %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// jsonSeqs parses --json output and returns each line's seq, in order.
func jsonSeqs(t *testing.T, out string) []int {
	t.Helper()
	var seqs []int
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		var e api.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("output line is not an event: %q: %v", line, err)
		}
		seqs = append(seqs, e.Seq)
	}
	return seqs
}

// seqRange returns lo..hi inclusive.
func seqRange(lo, hi int) []int {
	var s []int
	for i := lo; i <= hi; i++ {
		s = append(s, i)
	}
	return s
}

// --json is for programs. It must hand over the log's own bytes — not a
// re-encoding — so a field a newer aoa adds to the envelope reaches the
// consumer even through an older binary.
func TestEventsJSONIsLedgerVerbatim(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "replay", args: []string{"--json", "replay"}},
		{name: "flag after the subcommand", args: []string{"replay", "--json"}},
		{name: "tail of everything", args: []string{"tail", "--json", "--count", "0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, led := eventsWorkspace(t, 2)
			// A line from a newer writer: an envelope field api.Event does not
			// have, and < and >, which json.Marshal would escape as \u003c, \u003e.
			future := `{"seq":3,"type":"Heartbeat","ts":"2026-09-18T00:00:00Z","actor":"<ci>","trace_id":"4bf92f35","payload":{"worker":"w"}}` + "\n"
			path := filepath.Join(root, ".aoa", "events.jsonl")
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatalf("open log: %v", err)
			}
			if _, err := f.WriteString(future); err != nil {
				t.Fatalf("write future line: %v", err)
			}
			if err := f.Close(); err != nil {
				t.Fatalf("close log: %v", err)
			}
			if _, err := led.Read(); err != nil {
				t.Fatalf("the future line must still replay: %v", err)
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read log: %v", err)
			}

			got := runEvents(t, append([]string{"--path", root}, tt.args...)...)
			if got != string(want) {
				t.Errorf("--json output is not the log verbatim.\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

// --since N is a cursor: every event after N, however many, so a front door
// can resume from the last seq it received without missing or repeating one.
func TestEventsSinceIsExclusiveAndResumable(t *testing.T) {
	const n = 25 // more than tail's default 20, which --since must not apply
	tests := []struct {
		name string
		args []string
		want []int
	}{
		{name: "since 0 is every event", args: []string{"--since", "0"}, want: seqRange(1, n)},
		{name: "since k is exclusive", args: []string{"--since", "22"}, want: seqRange(23, n)},
		{name: "since the last seq is nothing", args: []string{"--since", "25"}},
		{name: "since past the end is nothing", args: []string{"--since", "99"}},
		{name: "replay selects the same events", args: []string{"replay", "--since", "22"}, want: seqRange(23, n)},
		{name: "type filters the output, not the cursor", args: []string{"--since", "20", "--type", "Heartbeat"}, want: []int{22, 24}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := eventsWorkspace(t, n)
			got := jsonSeqs(t, runEvents(t, append([]string{"--path", root, "--json"}, tt.args...)...))
			if !slices.Equal(got, tt.want) {
				t.Errorf("seqs = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("resuming from the last seq received yields exactly the new events", func(t *testing.T) {
		root, led := eventsWorkspace(t, n)
		first := jsonSeqs(t, runEvents(t, "--path", root, "--json", "--since", "0"))
		if len(first) != n {
			t.Fatalf("first read got %d events, want %d", len(first), n)
		}
		appendEvents(t, led, 2)
		cursor := strconv.Itoa(first[len(first)-1])
		got := jsonSeqs(t, runEvents(t, "--path", root, "--json", "--since", cursor))
		if want := []int{n + 1, n + 2}; !slices.Equal(got, want) {
			t.Errorf("resumed from %s: seqs = %v, want %v", cursor, got, want)
		}
	})
}

// --since means every event after N. An explicit --count would silently cut
// that short, so the pair is refused rather than half-honoured.
func TestEventsSinceRejectsExplicitCount(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string // "" = must succeed
	}{
		{name: "since with count", args: []string{"--since", "3", "--count", "5"}, wantErr: "--count"},
		{name: "count before the subcommand, since after", args: []string{"--count", "5", "tail", "--since", "3"}, wantErr: "--count"},
		{name: "count 0 is still explicit", args: []string{"--since", "3", "--count", "0"}, wantErr: "--count"},
		{name: "negative since", args: []string{"--since", "-1"}, wantErr: "--since"},
		{name: "since alone", args: []string{"--since", "3"}},
		{name: "count alone", args: []string{"--count", "5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := eventsWorkspace(t, 4)
			var err error
			captureStdout(t, func() { err = cmdEvents(append([]string{"--path", root}, tt.args...)) })
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("accepted %v, want an error naming %s", tt.args, tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("error %q should name %s", err, tt.wantErr)
			}
		})
	}
}

// syncBuffer is a bytes.Buffer that followEvents can write from its goroutine
// while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// lineCount is how many newline-terminated lines b holds.
func (b *syncBuffer) lineCount() int { return strings.Count(b.String(), "\n") }

// waitForLines waits until out holds n lines, failing the test if it takes
// too long or overshoots.
func waitForLines(t *testing.T, out *syncBuffer, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for out.lineCount() < n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d lines; have:\n%s", n, out.String())
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := out.lineCount(); got != n {
		t.Fatalf("got %d lines, want %d:\n%s", got, n, out.String())
	}
}

// appendToLog writes b to the end of the log at path, bypassing the Ledger, as
// a writer part-way through a line leaves it.
func appendToLog(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	if _, err := f.Write(b); err != nil {
		t.Fatalf("write log: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}
}

// startFollow runs followEvents over the log at path, through its own handle
// as a separate process would, and returns its output and a stop function that
// cancels it and returns its result.
func startFollow(t *testing.T, path string, since int) (*syncBuffer, func() error) {
	t.Helper()
	led, err := ledger.Open(path)
	if err != nil {
		t.Fatalf("open follower's ledger: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- followEvents(ctx, led, since, "", formatJSON, out, 2*time.Millisecond) }()
	stop := func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("followEvents did not return after its context was cancelled")
			return nil
		}
	}
	t.Cleanup(func() { cancel() })
	return out, stop
}

// --follow must print what other processes append, in order, and never a line
// a writer is still in the middle of.
func TestFollowEmitsAppendsInOrderWithoutPartialLines(t *testing.T) {
	root, writer := eventsWorkspace(t, 2)
	path := filepath.Join(root, ".aoa", "events.jsonl")
	out, stop := startFollow(t, path, 0)
	waitForLines(t, out, 2) // what was already there

	appendEvents(t, writer, 2) // seqs 3 and 4, through another handle
	waitForLines(t, out, 4)

	// Seq 5, half written. Many polls see it; none may print it.
	e, err := api.NewEvent(api.Merged, "test", api.MergedPayload{TicketID: "t1", Worker: "w", Commit: "abc"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	e.Seq = 5
	line, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	half := len(line) / 2
	appendToLog(t, path, line[:half])
	time.Sleep(50 * time.Millisecond)
	if got := out.String(); strings.Count(got, "\n") != 4 || !strings.HasSuffix(got, "\n") {
		t.Fatalf("printed part of a half-written line:\n%s", got)
	}
	appendToLog(t, path, append(slices.Clone(line[half:]), '\n'))
	waitForLines(t, out, 5)

	appendEvents(t, writer, 1) // seq 6, after the raw write
	waitForLines(t, out, 6)

	if err := stop(); err != nil {
		t.Errorf("followEvents returned %v on cancel, want nil", err)
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if got := out.String(); got != string(want) {
		t.Errorf("followed output is not the log, in order and whole.\n got: %q\nwant: %q", got, want)
	}
}

// A log truncated or replaced under a follower is read again from the start,
// and the seqs it already printed are not printed twice.
func TestFollowResumesBySeqWhenTheLogIsReplaced(t *testing.T) {
	root, writer := eventsWorkspace(t, 3)
	path := filepath.Join(root, ".aoa", "events.jsonl")
	out, stop := startFollow(t, path, 0)
	waitForLines(t, out, 3)

	// Restore an older copy of the log: just its first event.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if err := os.WriteFile(path, data[:bytes.IndexByte(data, '\n')+1], 0o644); err != nil {
		t.Fatalf("truncate log: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // the follower sees the log shrink
	appendEvents(t, writer, 3)        // seqs 2, 3 again, then the new 4
	waitForLines(t, out, 4)

	if err := stop(); err != nil {
		t.Fatalf("followEvents returned %v; a replaced log must be followed, not fatal", err)
	}
	if got := jsonSeqs(t, out.String()); !slices.Equal(got, []int{1, 2, 3, 4}) {
		t.Errorf("seqs = %v, want [1 2 3 4]: each once, in order", got)
	}
}

func TestEventsRejectsANonPositivePoll(t *testing.T) {
	for _, poll := range []string{"0s", "-1s"} {
		t.Run(poll, func(t *testing.T) {
			root, _ := eventsWorkspace(t, 1)
			err := cmdEvents([]string{"--path", root, "--follow", "--poll", poll})
			if err == nil || !strings.Contains(err.Error(), "--poll") {
				t.Errorf("err = %v, want one naming --poll", err)
			}
		})
	}
}
