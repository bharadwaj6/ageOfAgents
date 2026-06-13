package diagnose

import (
	"testing"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// seq builds a sequenced event stream, mirroring the helper in internal/invariant.
type seq struct {
	t      *testing.T
	events []api.Event
	n      int
}

func newSeq(t *testing.T) *seq { return &seq{t: t} }

func (s *seq) add(typ api.EventType, payload any) *seq {
	s.t.Helper()
	e, err := api.NewEvent(typ, "orchestrator", payload)
	if err != nil {
		s.t.Fatalf("NewEvent: %v", err)
	}
	s.n++
	e.Seq = s.n
	s.events = append(s.events, e)
	return s
}

// healthy is a complete, correct goal->ticket->merge history (zero failure modes).
func healthy(t *testing.T) []api.Event {
	return newSeq(t).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "do it"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "g1-impl", GoalID: "g1", Title: "impl", IdempotencyKey: "g1:impl"}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "g1-impl"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g1-impl", Worker: "w"}).
		add(api.WorkStarted, api.WorkStartedPayload{TicketID: "g1-impl", Worker: "w"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g1-impl", Worker: "w", Commit: "c"}).
		add(api.VerificationPassed, api.VerificationPassedPayload{TicketID: "g1-impl", Worker: "w"}).
		add(api.Merged, api.MergedPayload{TicketID: "g1-impl", Worker: "w", Commit: "c"}).
		events
}

// find returns the finding for a mode (the histogram always carries every mode).
func find(t *testing.T, r Report, m Mode) Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.Mode == m {
			return f
		}
	}
	t.Fatalf("mode %q missing from report", m)
	return Finding{}
}

func TestClassifyHealthyRunIsClean(t *testing.T) {
	r := Classify(healthy(t))
	if got := len(r.Findings); got != len(modeOrder) {
		t.Fatalf("expected %d findings (one per mode), got %d", len(modeOrder), got)
	}
	if r.Total() != 0 {
		t.Fatalf("healthy run should have 0 total failures, got %d: %+v", r.Total(), r.Findings)
	}
	// Order is deterministic.
	for i, m := range modeOrder {
		if r.Findings[i].Mode != m {
			t.Fatalf("finding %d: want mode %q, got %q", i, m, r.Findings[i].Mode)
		}
	}
}

func TestClassifyStepRepetition(t *testing.T) {
	events := newSeq(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "k1"}).
		add(api.VerificationPassed, api.VerificationPassedPayload{TicketID: "t1"}).
		add(api.Merged, api.MergedPayload{TicketID: "t1", Commit: "a"}).
		add(api.Merged, api.MergedPayload{TicketID: "t1", Commit: "b"}). // re-merged
		events
	f := find(t, Classify(events), StepRepetition)
	if f.Count != 1 || len(f.Tickets) != 1 || f.Tickets[0] != "t1" {
		t.Fatalf("step repetition: want count 1 ticket t1, got %+v", f)
	}
}

func TestClassifyPrematureTermination(t *testing.T) {
	// A ticket that fails on its own merit (no dependency) is a premature give-up.
	events := newSeq(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "k1"}).
		add(api.TicketFailed, api.TicketFailedPayload{TicketID: "t1", Reason: "attempts exhausted"}).
		events
	r := Classify(events)
	if f := find(t, r, PrematureTermination); f.Count != 1 {
		t.Fatalf("premature termination: want 1, got %+v", f)
	}
	if f := find(t, r, DeadDependencyStall); f.Count != 0 {
		t.Fatalf("dead-dependency should be 0 for a self-failure, got %+v", f)
	}
}

func TestClassifyDeadDependencyStall(t *testing.T) {
	// t1 fails; t2 depends on t1 and is left pending => blocked by dead dependency.
	events := newSeq(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", Title: "base", IdempotencyKey: "k1"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t2", Title: "dep", DependsOn: []string{"t1"}, IdempotencyKey: "k2"}).
		add(api.TicketFailed, api.TicketFailedPayload{TicketID: "t1", Reason: "boom"}).
		events
	r := Classify(events)
	f := find(t, r, DeadDependencyStall)
	if f.Count != 1 || len(f.Tickets) != 1 || f.Tickets[0] != "t2" {
		t.Fatalf("dead-dependency: want count 1 ticket t2, got %+v", f)
	}
	if f := find(t, r, PrematureTermination); f.Count != 1 { // t1 itself is a premature give-up
		t.Fatalf("premature termination: want 1 (t1), got %+v", f)
	}
}

func TestClassifyRetryChurn(t *testing.T) {
	events := newSeq(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "k1"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "t1", Commit: "c"}).
		add(api.VerificationFailed, api.VerificationFailedPayload{TicketID: "t1", Reason: "tests"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "t1", Commit: "c2"}).
		add(api.VerificationFailed, api.VerificationFailedPayload{TicketID: "t1", Reason: "tests"}).
		events
	f := find(t, Classify(events), RetryChurn)
	if f.Count != 2 {
		t.Fatalf("retry churn: want 2 rejections, got %+v", f)
	}
}

func TestClassifyMissingVerification(t *testing.T) {
	events := newSeq(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "k1"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "t1", Commit: "c"}).
		add(api.Merged, api.MergedPayload{TicketID: "t1", Commit: "c"}). // no VerificationPassed
		events
	f := find(t, Classify(events), MissingVerification)
	if f.Count != 1 {
		t.Fatalf("missing verification: want 1, got %+v", f)
	}
}

func TestClassifyWorkerStall(t *testing.T) {
	events := newSeq(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "k1"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "t1", Worker: "w"}).
		add(api.WorkerStalled, api.WorkerStalledPayload{TicketID: "t1", Worker: "w"}).
		add(api.WorkerRestarted, api.WorkerRestartedPayload{TicketID: "t1", Worker: "w"}).
		events
	f := find(t, Classify(events), WorkerStall)
	if f.Count != 1 || len(f.Tickets) != 1 || f.Tickets[0] != "t1" {
		t.Fatalf("worker stall: want count 1 ticket t1, got %+v", f)
	}
}

func TestClassifyReplayErrorOnCorruptLog(t *testing.T) {
	bogus, err := api.NewEvent(api.EventType("Bogus"), "x", nil)
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	bogus.Seq = 1
	r := Classify([]api.Event{bogus})
	if len(r.Findings) != 1 || r.Findings[0].Mode != ReplayError {
		t.Fatalf("expected a single replay_error finding, got %+v", r.Findings)
	}
}
