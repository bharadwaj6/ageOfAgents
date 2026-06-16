package ledger

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

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
