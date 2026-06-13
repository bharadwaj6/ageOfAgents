package state

import (
	"testing"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// build is a tiny helper to construct a sequenced event stream for folding.
type build struct {
	t      *testing.T
	events []api.Event
	seq    int
}

func newBuild(t *testing.T) *build { return &build{t: t} }

func (b *build) add(typ api.EventType, payload any) *build {
	b.t.Helper()
	e, err := api.NewEvent(typ, "test", payload)
	if err != nil {
		b.t.Fatalf("NewEvent: %v", err)
	}
	b.seq++
	e.Seq = b.seq
	b.events = append(b.events, e)
	return b
}

func (b *build) fold() *State {
	b.t.Helper()
	s, err := Fold(b.events)
	if err != nil {
		b.t.Fatalf("Fold: %v", err)
	}
	return s
}

func TestHappyPathFold(t *testing.T) {
	s := newBuild(t).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "do it"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "impl", IdempotencyKey: "k1"}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "t1"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "t1", Worker: "w1"}).
		add(api.WorkStarted, api.WorkStartedPayload{TicketID: "t1", Worker: "w1", Worktree: "/wt"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "t1", Worker: "w1", Branch: "aoa/t1", Commit: "c1"}).
		add(api.VerificationPassed, api.VerificationPassedPayload{TicketID: "t1", Worker: "w1"}).
		add(api.Merged, api.MergedPayload{TicketID: "t1", Worker: "w1", Commit: "c1"}).
		fold()

	tk := s.Tickets["t1"]
	if tk == nil {
		t.Fatal("ticket t1 missing")
	}
	if tk.Status != StatusMerged {
		t.Errorf("status = %s, want merged", tk.Status)
	}
	if tk.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", tk.Attempts)
	}
	if tk.Commit != "c1" {
		t.Errorf("commit = %q, want c1", tk.Commit)
	}
	if !s.Settled() {
		t.Error("expected settled")
	}
	if s.LastSeq != 8 {
		t.Errorf("LastSeq = %d, want 8", s.LastSeq)
	}
}

func TestIdempotentTicketCreation(t *testing.T) {
	s := newBuild(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "same"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t2", Title: "x-dup", IdempotencyKey: "same"}).
		fold()

	if len(s.Tickets) != 1 {
		t.Fatalf("got %d tickets, want 1 (idempotency key should dedupe)", len(s.Tickets))
	}
	if _, ok := s.Tickets["t1"]; !ok {
		t.Error("first ticket should win")
	}
	if _, ok := s.Tickets["t2"]; ok {
		t.Error("duplicate ticket should be ignored")
	}
}

func TestDependencyReadiness(t *testing.T) {
	b := newBuild(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "plan", Title: "plan", IdempotencyKey: "kp"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "impl", Title: "impl", DependsOn: []string{"plan"}, IdempotencyKey: "ki"})
	s := b.fold()

	// Only the dependency-free ticket should be newly ready.
	ready := s.NewlyReady()
	if len(ready) != 1 || ready[0].ID != "plan" {
		t.Fatalf("NewlyReady = %v, want [plan]", ids(ready))
	}

	// Advance "plan" to merged, then "impl" becomes ready.
	s = b.
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "plan"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "plan", Worker: "w1"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "plan", Worker: "w1", Commit: "c"}).
		add(api.Merged, api.MergedPayload{TicketID: "plan", Worker: "w1", Commit: "c"}).
		fold()

	ready = s.NewlyReady()
	if len(ready) != 1 || ready[0].ID != "impl" {
		t.Fatalf("after plan merged, NewlyReady = %v, want [impl]", ids(ready))
	}
	if !s.DepsSatisfied(s.Tickets["impl"]) {
		t.Error("impl deps should be satisfied")
	}
}

func TestVerificationFailedRetriesTicket(t *testing.T) {
	s := newBuild(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "k"}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "t1"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "t1", Worker: "w1"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "t1", Worker: "w1", Commit: "c1"}).
		add(api.VerificationFailed, api.VerificationFailedPayload{TicketID: "t1", Worker: "w1", Reason: "tests failed"}).
		fold()

	tk := s.Tickets["t1"]
	if tk.Status != StatusReady {
		t.Errorf("status = %s, want ready (re-dispatchable)", tk.Status)
	}
	if tk.Worker != "" || tk.Commit != "" {
		t.Errorf("proposal fields should be cleared, got worker=%q commit=%q", tk.Worker, tk.Commit)
	}
	if s.Settled() {
		t.Error("should not be settled while a ticket is ready")
	}
	if len(s.ReadyTickets()) != 1 {
		t.Errorf("expected 1 ready ticket for retry")
	}
}

func TestActiveCountBackpressure(t *testing.T) {
	s := newBuild(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", Title: "a", IdempotencyKey: "k1"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t2", Title: "b", IdempotencyKey: "k2"}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "t1"}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "t2"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "t1", Worker: "w1"}).
		add(api.WorkStarted, api.WorkStartedPayload{TicketID: "t1", Worker: "w1"}).
		fold()

	if got := s.ActiveCount(); got != 1 {
		t.Errorf("ActiveCount = %d, want 1", got)
	}
	if got := len(s.ReadyTickets()); got != 1 {
		t.Errorf("ReadyTickets = %d, want 1 (t2 still ready)", got)
	}
}

func TestUnknownEventErrors(t *testing.T) {
	e, _ := api.NewEvent(api.EventType("Bogus"), "x", nil)
	if _, err := Fold([]api.Event{e}); err == nil {
		t.Error("expected error for unknown event type")
	}
}

func ids(ts []*Ticket) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}
