package metrics

import (
	"testing"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/state"
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

func TestComputeSumsTokens(t *testing.T) {
	s := newStream(t).
		add(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: "g1", Text: "build"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "root", GoalID: "g1", Title: "root", IdempotencyKey: "g1:impl"}).
		add(api.TicketDecomposed, "orchestrator", api.TicketDecomposedPayload{TicketID: "root", Children: []string{"c"}, Tokens: 300}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "c", GoalID: "g1", Title: "child", CreatedBy: "w", Depth: 1, IdempotencyKey: "g1:c"}).
		add(api.TicketReady, "orchestrator", api.TicketReadyPayload{TicketID: "c"}).
		add(api.TicketClaimed, "orchestrator", api.TicketClaimedPayload{TicketID: "c", Worker: "w"}).
		add(api.WorkStarted, "orchestrator", api.WorkStartedPayload{TicketID: "c", Worker: "w"}).
		add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: "c", Commit: "x", Tokens: 1200}).
		add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: "c"}).
		add(api.Merged, "orchestrator", api.MergedPayload{TicketID: "c", Commit: "x"})

	m := Compute(s.events)
	if m.TokensTotal != 1500 {
		t.Fatalf("TokensTotal = %d, want 1500 (1200 proposal + 300 decompose)", m.TokensTotal)
	}
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

func TestGraphShapes(t *testing.T) {
	// A diamond: root decomposes into 4 children at depth 1.
	s := newStream(t).
		add(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: "g1", Text: "build"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "root", GoalID: "g1", Title: "root", IdempotencyKey: "g1:impl"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "t", GoalID: "g1", Title: "types", CreatedBy: "w", Depth: 1, IdempotencyKey: "g1:t"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "b", GoalID: "g1", Title: "backend", DependsOn: []string{"t"}, CreatedBy: "w", Depth: 1, IdempotencyKey: "g1:b"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "f", GoalID: "g1", Title: "frontend", DependsOn: []string{"t"}, CreatedBy: "w", Depth: 1, IdempotencyKey: "g1:f"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "e", GoalID: "g1", Title: "e2e", DependsOn: []string{"b", "f"}, CreatedBy: "w", Depth: 1, IdempotencyKey: "g1:e"}).
		add(api.TicketDecomposed, "orchestrator", api.TicketDecomposedPayload{TicketID: "root", Children: []string{"t", "b", "f", "e"}})

	st, err := state.Fold(s.events)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	shapes := GraphShapes(st)
	if len(shapes) != 1 {
		t.Fatalf("GraphShapes len = %d, want 1", len(shapes))
	}
	got := shapes[0]
	want := GraphShape{GoalID: "g1", Tickets: 5, MaxDepth: 1, MaxFanOut: 4}
	if got != want {
		t.Errorf("GraphShape = %+v, want %+v", got, want)
	}
}

func TestGraphShapesSingleTicket(t *testing.T) {
	// A goal with one undecomposed ticket: zero depth, zero fan-out.
	s := newStream(t).
		add(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: "g1", Text: "tiny"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "g1-impl", GoalID: "g1", Title: "impl", IdempotencyKey: "g1:impl"})

	st, err := state.Fold(s.events)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	shapes := GraphShapes(st)
	if len(shapes) != 1 {
		t.Fatalf("GraphShapes len = %d, want 1", len(shapes))
	}
	want := GraphShape{GoalID: "g1", Tickets: 1, MaxDepth: 0, MaxFanOut: 0}
	if shapes[0] != want {
		t.Errorf("GraphShape = %+v, want %+v", shapes[0], want)
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

func TestComputeCostBreakdown(t *testing.T) {
	// Two tickets in one goal, each from a different model, with known tokens.
	s := newStream(t).
		add(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: "g1", Text: "build"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "a", IdempotencyKey: "g1:a"}).
		add(api.TicketReady, "orchestrator", api.TicketReadyPayload{TicketID: "t1"}).
		add(api.TicketClaimed, "orchestrator", api.TicketClaimedPayload{TicketID: "t1", Worker: "w"}).
		add(api.WorkStarted, "orchestrator", api.WorkStartedPayload{TicketID: "t1", Worker: "w"}).
		add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: "t1", Commit: "c1", Tokens: 1000, Model: "modelA"}).
		add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: "t1"}).
		add(api.Merged, "orchestrator", api.MergedPayload{TicketID: "t1", Commit: "c1"}).
		add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: "t2", GoalID: "g1", Title: "b", IdempotencyKey: "g1:b"}).
		add(api.TicketReady, "orchestrator", api.TicketReadyPayload{TicketID: "t2"}).
		add(api.TicketClaimed, "orchestrator", api.TicketClaimedPayload{TicketID: "t2", Worker: "w"}).
		add(api.WorkStarted, "orchestrator", api.WorkStartedPayload{TicketID: "t2", Worker: "w"}).
		add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: "t2", Commit: "c2", Tokens: 2000, Model: "modelB"}).
		add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: "t2"}).
		add(api.Merged, "orchestrator", api.MergedPayload{TicketID: "t2", Commit: "c2"})

	m := Compute(s.events)

	if m.TokensTotal != 3000 {
		t.Fatalf("TokensTotal = %d, want 3000", m.TokensTotal)
	}
	if got := m.TokensByModel; got["modelA"] != 1000 || got["modelB"] != 2000 || len(got) != 2 {
		t.Errorf("TokensByModel = %v, want {modelA:1000 modelB:2000}", got)
	}

	byTicket := map[string]TicketCost{}
	for _, tc := range m.PerTicket {
		byTicket[tc.TicketID] = tc
	}
	if tc := byTicket["t1"]; tc.Tokens != 1000 || tc.Model != "modelA" || tc.Status != "merged" || tc.GoalID != "g1" {
		t.Errorf("PerTicket[t1] = %+v, want tokens=1000 model=modelA status=merged goal=g1", tc)
	}
	// t1 spans TicketCreated(seq2) → Merged(seq8): 6 one-second ticks.
	if d := byTicket["t1"].DurationSeconds; d != 6 {
		t.Errorf("PerTicket[t1].DurationSeconds = %v, want 6", d)
	}
	if tc := byTicket["t2"]; tc.Tokens != 2000 || tc.Model != "modelB" {
		t.Errorf("PerTicket[t2] = %+v, want tokens=2000 model=modelB", tc)
	}

	if len(m.PerGoal) != 1 {
		t.Fatalf("PerGoal len = %d, want 1", len(m.PerGoal))
	}
	g := m.PerGoal[0]
	if g.GoalID != "g1" || g.Tokens != 3000 || g.Merged != 2 || g.Failed != 0 {
		t.Errorf("PerGoal[0] = %+v, want goal=g1 tokens=3000 merged=2 failed=0", g)
	}
	// Goal spans t1's first event (seq2) → t2's last event (seq15): 13 ticks.
	if g.DurationSeconds != 13 {
		t.Errorf("PerGoal[0].DurationSeconds = %v, want 13", g.DurationSeconds)
	}

	// $ = 1000/1e6*15 + 2000/1e6*5 = 0.025.
	cost := USD(m.TokensByModel, map[string]float64{"modelA": 15, "modelB": 5})
	if cost < 0.02499 || cost > 0.02501 {
		t.Errorf("USD = %v, want ~0.025", cost)
	}
	// An unpriced model contributes 0.
	if c := USD(map[string]int{"unknown": 9999}, nil); c != 0 {
		t.Errorf("USD(unpriced) = %v, want 0", c)
	}
}

func TestComputeRegressionEscapeRate(t *testing.T) {
	// Two merges; the broader Shadow set flagged the second as an escape.
	merge := func(s *stream, id string) *stream {
		return s.
			add(api.TicketCreated, "orchestrator", api.TicketCreatedPayload{TicketID: id, GoalID: "g1", Title: id, IdempotencyKey: "g1:" + id}).
			add(api.TicketReady, "orchestrator", api.TicketReadyPayload{TicketID: id}).
			add(api.TicketClaimed, "orchestrator", api.TicketClaimedPayload{TicketID: id, Worker: "w"}).
			add(api.WorkStarted, "orchestrator", api.WorkStartedPayload{TicketID: id, Worker: "w"}).
			add(api.ProposalSubmitted, "orchestrator", api.ProposalSubmittedPayload{TicketID: id, Commit: id}).
			add(api.VerificationPassed, "orchestrator", api.VerificationPassedPayload{TicketID: id}).
			add(api.Merged, "orchestrator", api.MergedPayload{TicketID: id, Commit: id})
	}
	s := newStream(t).add(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: "g1", Text: "x"})
	merge(s, "a")
	merge(s, "b").add(api.RegressionEscaped, "orchestrator", api.RegressionEscapedPayload{TicketID: "b", Reason: "broader suite failed"})

	m := Compute(s.events)
	if m.Merged != 2 {
		t.Fatalf("Merged = %d, want 2", m.Merged)
	}
	if m.RegressionEscapes != 1 {
		t.Errorf("RegressionEscapes = %d, want 1", m.RegressionEscapes)
	}
	if m.RegressionEscapeRate != 0.5 {
		t.Errorf("RegressionEscapeRate = %v, want 0.5 (1 of 2 merges)", m.RegressionEscapeRate)
	}
}
