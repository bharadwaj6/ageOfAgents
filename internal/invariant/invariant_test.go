package invariant

import (
	"strings"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
	"github.com/stretchr/testify/require"
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

func TestApprovalGateFlagsMergeWithoutGrant(t *testing.T) {
	// A proposal was parked for approval but merged without ApprovalGranted.
	bad := newSeq(t).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "k"}).
		add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: "t1", Commit: "c"}).
		add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: "t1"}).
		add(api.ApprovalRequested, "orchestrator", api.ApprovalRequestedPayload{TicketID: "t1"}).
		add(api.Merged, "orchestrator", api.MergedPayload{TicketID: "t1", Commit: "c"}).
		events
	if vs := ApprovalGate(bad); len(vs) == 0 {
		t.Error("expected an ApprovalGate violation for a merge without approval")
	}

	// With ApprovalGranted before the merge, the gate is satisfied.
	good := newSeq(t).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "k"}).
		add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: "t1", Commit: "c"}).
		add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: "t1"}).
		add(api.ApprovalRequested, "orchestrator", api.ApprovalRequestedPayload{TicketID: "t1"}).
		add(api.ApprovalGranted, "human", api.ApprovalGrantedPayload{TicketID: "t1"}).
		add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: "t1"}).
		add(api.Merged, "orchestrator", api.MergedPayload{TicketID: "t1", Commit: "c"}).
		events
	if vs := ApprovalGate(good); len(vs) != 0 {
		t.Errorf("approved merge should pass the gate, got: %v", vs)
	}
}

func TestMonotonicGaplessSeqFlagsGap(t *testing.T) {
	e1, err := api.NewEvent(api.Heartbeat, "x", api.HeartbeatPayload{})
	require.NoError(t, err)
	e2, err := api.NewEvent(api.Heartbeat, "x", api.HeartbeatPayload{})
	require.NoError(t, err)
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

func TestCancelHonored(t *testing.T) {
	// Each history submits g1 with ticket t1 (and g2 with t2), then runs steps.
	type step func(s *seq) *seq
	propose := func(id string) step {
		return func(s *seq) *seq {
			return s.add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: id, Worker: "w", Commit: "c"})
		}
	}
	park := func(id string) step {
		return func(s *seq) *seq {
			return s.add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: id, Worker: "w"}).
				add(api.ApprovalRequested, "orchestrator", api.ApprovalRequestedPayload{TicketID: id, Worker: "w"})
		}
	}
	approve := func(id string) step {
		return func(s *seq) *seq {
			return s.add(api.ApprovalGranted, "human", api.ApprovalGrantedPayload{TicketID: id})
		}
	}
	merge := func(id string) step {
		return func(s *seq) *seq {
			return s.add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: id, Worker: "w"}).
				add(api.Merged, "orchestrator", api.MergedPayload{TicketID: id, Worker: "w", Commit: "m"})
		}
	}
	fail := func(id string) step {
		return func(s *seq) *seq {
			return s.add(api.TicketFailed, "orchestrator", api.TicketFailedPayload{TicketID: id, Worker: "w", Reason: "goal cancelled"})
		}
	}
	cancel := func(goal string) step {
		return func(s *seq) *seq {
			return s.add(api.GoalCancelled, "human", api.GoalCancelledPayload{GoalID: goal})
		}
	}

	tests := []struct {
		name      string
		steps     []step
		violation bool
	}{
		{name: "proposal failed after the cancel", steps: []step{propose("t1"), cancel("g1"), fail("t1")}},
		{name: "merged before the cancel", steps: []step{propose("t1"), merge("t1"), cancel("g1")}},
		{name: "merge already under way when the cancel landed", steps: []step{propose("t1"), cancel("g1"), merge("t1")}},
		{name: "another goal cancelled", steps: []step{cancel("g2"), propose("t1"), merge("t1")}},
		{name: "proposed after the cancel, then merged", steps: []step{cancel("g1"), propose("t1"), merge("t1")}, violation: true},
		{name: "approved after the cancel, then merged", steps: []step{propose("t1"), park("t1"), cancel("g1"), approve("t1"), merge("t1")}, violation: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSeq(t).
				add(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: "g1", Text: "one"}).
				add(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: "g2", Text: "two"}).
				add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "one", IdempotencyKey: "k1"}).
				add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "t2", GoalID: "g2", Title: "two", IdempotencyKey: "k2"})
			for _, st := range tt.steps {
				s = st(s)
			}
			var got []Violation
			for _, v := range Check(s.events) {
				if v.Invariant == "CancelHonored" {
					got = append(got, v)
				}
			}
			if tt.violation && len(got) == 0 {
				t.Error("expected a CancelHonored violation from Check")
			}
			if !tt.violation && len(got) != 0 {
				t.Errorf("expected no CancelHonored violation, got %v", got)
			}
		})
	}
}

func TestSettledExemptsACancelledGoal(t *testing.T) {
	// A Goal cancelled before the Scheduler picked it up never gets a ticket.
	events := newSeq(t).
		add(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: "g1", Text: "one"}).
		add(api.GoalCancelled, "human", api.GoalCancelledPayload{GoalID: "g1"}).
		events
	if vs := Settled(events); len(vs) != 0 {
		t.Errorf("a cancelled goal without tickets should be settled, got: %v", vs)
	}
	uncancelled := newSeq(t).
		add(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: "g1", Text: "one"}).
		events
	if vs := Settled(uncancelled); len(vs) == 0 {
		t.Error("a goal with no tickets that was never cancelled is not settled")
	}
}

func TestDeliveredOnceAndVerified(t *testing.T) {
	// Each history submits g1 with tickets t1 and t2, then runs steps.
	type step func(s *seq) *seq
	merge := func(id, commit string) step {
		return func(s *seq) *seq {
			return s.add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: id, Worker: "w", Commit: "cand-" + id}).
				add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: id, Worker: "w"}).
				add(api.Merged, "orchestrator", api.MergedPayload{TicketID: id, Worker: "w", Commit: commit, Branch: "aoa/g1"})
		}
	}
	deliver := func(commit string) step {
		return func(s *seq) *seq {
			return s.add(api.Delivered, "orchestrator", api.DeliveredPayload{GoalID: "g1", Branch: "aoa/g1", Commit: commit, URL: "https://example.test/pr/1"})
		}
	}
	deliveryFailed := func(s *seq) *seq {
		return s.add(api.DeliveryFailed, "orchestrator", api.DeliveryFailedPayload{GoalID: "g1", Branch: "aoa/g1", Reason: "push rejected"})
	}

	tests := []struct {
		name      string
		steps     []step
		violation bool
	}{
		{name: "delivered the last verified commit", steps: []step{merge("t1", "m1"), merge("t2", "m2"), deliver("m2")}},
		{name: "retried after a failed delivery", steps: []step{merge("t1", "m1"), merge("t2", "m2"), deliveryFailed, deliver("m2")}},
		{name: "delivered twice", steps: []step{merge("t1", "m1"), merge("t2", "m2"), deliver("m2"), deliver("m2")}, violation: true},
		{name: "delivered with a task still unmerged", steps: []step{merge("t1", "m1"), deliver("m1")}, violation: true},
		{name: "delivered before any merge", steps: []step{deliver("m0")}, violation: true},
		{name: "delivered a commit that is not the last merge", steps: []step{merge("t1", "m1"), merge("t2", "m2"), deliver("m1")}, violation: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSeq(t).
				add(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: "g1", Text: "one"}).
				add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "one", IdempotencyKey: "k1"}).
				add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "t2", GoalID: "g1", Title: "two", IdempotencyKey: "k2"})
			for _, st := range tt.steps {
				s = st(s)
			}
			var got []Violation
			for _, v := range Check(s.events) {
				if v.Invariant == "DeliveredOnceAndVerified" {
					got = append(got, v)
				}
			}
			if tt.violation && len(got) == 0 {
				t.Error("expected a DeliveredOnceAndVerified violation from Check")
			}
			if !tt.violation && len(got) != 0 {
				t.Errorf("expected no DeliveredOnceAndVerified violation, got %v", got)
			}
		})
	}
}
