package ledger

import (
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
	_, _ = l.Append(mustEvent(t, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "do it"}))
	_, _ = l.Append(mustEvent(t, api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "impl", IdempotencyKey: "k1"}))

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
	_, _ = l1.Append(mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
	_, _ = l1.Append(mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))

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
	l, _ := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	_, _ = l.Append(mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
	_, _ = l.Append(mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "b"}))

	count := 0
	sentinel := errSentinel{}
	err := l.Replay(func(api.Event) error {
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
	l, _ := Open(filepath.Join(t.TempDir(), "events.jsonl"))
	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = l.Append(mustEvent(t, api.Heartbeat, api.HeartbeatPayload{Worker: "a"}))
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
