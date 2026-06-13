package metrics

import (
	"testing"
	"time"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

type stream struct {
	t      *testing.T
	events []api.Event
	n      int
	clock  time.Time
}

func newStream(t *testing.T) *stream {
	return &stream{t: t, clock: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (s *stream) add(typ api.EventType, actor string, payload any) *stream {
	s.t.Helper()
	e, err := api.NewEvent(typ, actor, payload)
	if err != nil {
		s.t.Fatalf("NewEvent: %v", err)
	}
	s.n++
	e.Seq = s.n
	s.clock = s.clock.Add(time.Second)
	e.Timestamp = s.clock
	s.events = append(s.events, e)
	return s
}

func TestComputeDiamond(t *testing.T) {
	s := newStream(t).
		add(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: "g1", Text: "build"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "root", GoalID: "g1", Title: "root", IdempotencyKey: "g1:impl"}).
		add(api.TicketReady, "orchestrator", api.TicketReadyPayload{TicketID: "root"}).
		add(api.TicketClaimed, "orchestrator", api.TicketClaimedPayload{TicketID: "root", Worker: "w"}).
		add(api.WorkStarted, "orchestrator", api.WorkStartedPayload{TicketID: "root", Worker: "w"}).
		// root decomposes into a diamond.
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "t", GoalID: "g1", Title: "types", CreatedBy: "w", Depth: 1, IdempotencyKey: "g1:t"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "b", GoalID: "g1", Title: "backend", DependsOn: []string{"t"}, CreatedBy: "w", Depth: 1, IdempotencyKey: "g1:b"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "f", GoalID: "g1", Title: "frontend", DependsOn: []string{"t"}, CreatedBy: "w", Depth: 1, IdempotencyKey: "g1:f"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "e", GoalID: "g1", Title: "e2e", DependsOn: []string{"b", "f"}, CreatedBy: "w", Depth: 1, IdempotencyKey: "g1:e"}).
		add(api.TicketDecomposed, "orchestrator", api.TicketDecomposedPayload{TicketID: "root", Children: []string{"t", "b", "f", "e"}})

	// Merge t.
	s.add(api.TicketReady, "orchestrator", api.TicketReadyPayload{TicketID: "t"}).
		add(api.TicketClaimed, "orchestrator", api.TicketClaimedPayload{TicketID: "t", Worker: "w"}).
		add(api.WorkStarted, "orchestrator", api.WorkStartedPayload{TicketID: "t", Worker: "w"}).
		add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: "t", Commit: "c"}).
		add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: "t"}).
		add(api.Merged, "orchestrator", api.MergedPayload{TicketID: "t", Commit: "c"})

	// b and f run concurrently, then merge.
	s.add(api.TicketReady, "orchestrator", api.TicketReadyPayload{TicketID: "b"}).
		add(api.TicketReady, "orchestrator", api.TicketReadyPayload{TicketID: "f"}).
		add(api.TicketClaimed, "orchestrator", api.TicketClaimedPayload{TicketID: "b", Worker: "wb"}).
		add(api.TicketClaimed, "orchestrator", api.TicketClaimedPayload{TicketID: "f", Worker: "wf"}).
		add(api.WorkStarted, "orchestrator", api.WorkStartedPayload{TicketID: "b", Worker: "wb"}).
		add(api.WorkStarted, "orchestrator", api.WorkStartedPayload{TicketID: "f", Worker: "wf"}). // 2 concurrent here
		add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: "b", Commit: "cb"}).
		add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: "b"}).
		add(api.Merged, "orchestrator", api.MergedPayload{TicketID: "b", Commit: "cb"}).
		add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: "f", Commit: "cf"}).
		add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: "f"}).
		add(api.Merged, "orchestrator", api.MergedPayload{TicketID: "f", Commit: "cf"})

	// e merges last.
	s.add(api.TicketReady, "orchestrator", api.TicketReadyPayload{TicketID: "e"}).
		add(api.TicketClaimed, "orchestrator", api.TicketClaimedPayload{TicketID: "e", Worker: "we"}).
		add(api.WorkStarted, "orchestrator", api.WorkStartedPayload{TicketID: "e", Worker: "we"}).
		add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: "e", Commit: "ce"}).
		add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: "e"}).
		add(api.Merged, "orchestrator", api.MergedPayload{TicketID: "e", Commit: "ce"})

	m := Compute(s.events)

	if m.Goals != 1 {
		t.Errorf("Goals = %d, want 1", m.Goals)
	}
	if m.Merged != 4 {
		t.Errorf("Merged = %d, want 4", m.Merged)
	}
	if m.Decomposed != 1 {
		t.Errorf("Decomposed = %d, want 1", m.Decomposed)
	}
	if m.EmergentTickets != 4 {
		t.Errorf("EmergentTickets = %d, want 4", m.EmergentTickets)
	}
	if m.CoordinationSessions != 0 {
		t.Errorf("CoordinationSessions = %d, want 0 (deterministic scheduler)", m.CoordinationSessions)
	}
	if m.MergeCorrectness != 1.0 {
		t.Errorf("MergeCorrectness = %v, want 1.0", m.MergeCorrectness)
	}
	if m.StepRepetitions != 0 {
		t.Errorf("StepRepetitions = %d, want 0", m.StepRepetitions)
	}
	if m.MaxConcurrentWorkers != 2 {
		t.Errorf("MaxConcurrentWorkers = %d, want 2 (backend+frontend in parallel)", m.MaxConcurrentWorkers)
	}
	if m.CriticalPathDepth != 3 {
		t.Errorf("CriticalPathDepth = %d, want 3 (types->backend->e2e)", m.CriticalPathDepth)
	}
	if m.MeanAttemptsToMerge != 1.0 {
		t.Errorf("MeanAttemptsToMerge = %v, want 1.0", m.MeanAttemptsToMerge)
	}
}

func TestComputeRejectionRate(t *testing.T) {
	s := newStream(t).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "k"}).
		add(api.TicketReady, "orchestrator", api.TicketReadyPayload{TicketID: "t1"}).
		add(api.TicketClaimed, "orchestrator", api.TicketClaimedPayload{TicketID: "t1", Worker: "w"}).
		add(api.WorkStarted, "orchestrator", api.WorkStartedPayload{TicketID: "t1", Worker: "w"}).
		add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: "t1", Commit: "c"}).
		add(api.VerificationFailed, "orchestrator", api.VerificationFailedPayload{TicketID: "t1", Reason: "boom"}).
		add(api.TicketClaimed, "orchestrator", api.TicketClaimedPayload{TicketID: "t1", Worker: "w"}).
		add(api.WorkStarted, "orchestrator", api.WorkStartedPayload{TicketID: "t1", Worker: "w"}).
		add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: "t1", Commit: "c2"}).
		add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: "t1"}).
		add(api.Merged, "orchestrator", api.MergedPayload{TicketID: "t1", Commit: "c2"})

	m := Compute(s.events)
	if m.Merged != 1 {
		t.Fatalf("Merged = %d, want 1", m.Merged)
	}
	// 1 rejection, 1 merge -> 0.5
	if m.RejectedProposalRate != 0.5 {
		t.Errorf("RejectedProposalRate = %v, want 0.5", m.RejectedProposalRate)
	}
	if m.MeanAttemptsToMerge != 2.0 {
		t.Errorf("MeanAttemptsToMerge = %v, want 2.0", m.MeanAttemptsToMerge)
	}
}
