package invariant

import (
	"strings"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// seq builds a sequenced event stream for the checkers.
type seq struct {
	t      *testing.T
	events []api.Event
	n      int
}

func newSeq(t *testing.T) *seq { return &seq{t: t} }

func (s *seq) add(typ api.EventType, actor string, payload any) *seq {
	s.t.Helper()
	e, err := api.NewEvent(typ, actor, payload)
	if err != nil {
		s.t.Fatalf("NewEvent: %v", err)
	}
	s.n++
	e.Seq = s.n
	s.events = append(s.events, e)
	return s
}

// healthy is a complete, correct goal->ticket->merge history.
func healthy(t *testing.T) []api.Event {
	return newSeq(t).
		add(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: "g1", Text: "do it"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "g1-impl", GoalID: "g1", Title: "impl", IdempotencyKey: "g1:impl"}).
		add(api.TicketReady, "orchestrator", api.TicketReadyPayload{TicketID: "g1-impl"}).
		add(api.TicketClaimed, "orchestrator", api.TicketClaimedPayload{TicketID: "g1-impl", Worker: "w"}).
		add(api.WorkStarted, "orchestrator", api.WorkStartedPayload{TicketID: "g1-impl", Worker: "w"}).
		add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: "g1-impl", Worker: "w", Commit: "c"}).
		add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: "g1-impl", Worker: "w"}).
		add(api.Merged, "orchestrator", api.MergedPayload{TicketID: "g1-impl", Worker: "w", Commit: "c"}).
		events
}

func TestCheckPassesOnHealthyHistory(t *testing.T) {
	if vs := Check(healthy(t)); len(vs) != 0 {
		t.Fatalf("healthy history should have no violations, got: %v", vs)
	}
	if vs := Settled(healthy(t)); len(vs) != 0 {
		t.Errorf("healthy history should be settled, got: %v", vs)
	}
}

func TestMergeImpliesVerifiedFlagsUnverifiedMerge(t *testing.T) {
	// Merge without a preceding VerificationPassed for the proposal.
	events := newSeq(t).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "k"}).
		add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: "t1", Commit: "c"}).
		add(api.Merged, "orchestrator", api.MergedPayload{TicketID: "t1", Commit: "c"}).
		events
	if vs := MergeImpliesVerified(events); len(vs) == 0 {
		t.Error("expected a MergeImpliesVerified violation for an unverified merge")
	}
}

func TestMergedAtMostOnceFlagsDoubleMergeAndForeignActor(t *testing.T) {
	dbl := newSeq(t).
		add(api.Merged, "orchestrator", api.MergedPayload{TicketID: "t1", Commit: "a"}).
		add(api.Merged, "orchestrator", api.MergedPayload{TicketID: "t1", Commit: "b"}).
		events
	if vs := MergedAtMostOnceByQueue(dbl); len(vs) == 0 {
		t.Error("expected a violation for a double merge")
	}
	foreign := newSeq(t).
		add(api.Merged, "rogue", api.MergedPayload{TicketID: "t1", Commit: "a"}).
		events
	if vs := MergedAtMostOnceByQueue(foreign); len(vs) == 0 {
		t.Error("expected a violation for a non-queue merge actor")
	}
}

func TestAcyclicGraphFlagsCycle(t *testing.T) {
	events := newSeq(t).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "a", Title: "a", DependsOn: []string{"b"}, IdempotencyKey: "ka"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "b", Title: "b", DependsOn: []string{"a"}, IdempotencyKey: "kb"}).
		events
	if vs := AcyclicGraph(events); len(vs) == 0 {
		t.Error("expected an AcyclicGraph violation for a 2-cycle")
	}
}

func TestMonotonicGaplessSeqFlagsGap(t *testing.T) {
	e1, _ := api.NewEvent(api.Heartbeat, "x", api.HeartbeatPayload{})
	e1.Seq = 1
	e2, _ := api.NewEvent(api.Heartbeat, "x", api.HeartbeatPayload{})
	e2.Seq = 3 // gap
	if vs := MonotonicGaplessSeq([]api.Event{e1, e2}); len(vs) == 0 {
		t.Error("expected a violation for a sequence gap")
	}
}

func TestViolationString(t *testing.T) {
	v := Violation{"X", "boom"}
	if !strings.Contains(v.String(), "X") || !strings.Contains(v.String(), "boom") {
		t.Errorf("unexpected string: %s", v)
	}
}
