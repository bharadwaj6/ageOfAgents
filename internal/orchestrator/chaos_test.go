package orchestrator

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/invariant"
	"github.com/bharadwaj6/ageOfAgents/internal/mergequeue"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// chaosGate rejects any tree containing the bad-verify sentinel, so a
// FaultBadVerify proposal merges, fails verification, and is rolled back —
// exercising the merge queue's rollback path while keeping main green.
func chaosGate() verify.Verifier {
	return verify.Verifier{Commands: []verify.Command{{"sh", "-c", "test ! -e " + agent.BadVerifyFile}}}
}

// diamondPlan decomposes the root ticket into a diamond so each chaos run
// exercises emergent decomposition, parallelism, and a dependency join.
func diamondPlan() map[string][]agent.Subtask {
	return map[string][]agent.Subtask{
		"Implement: build app": {
			{LocalID: "types", Title: "shared types", IdempotencyKey: "g1:types"},
			{LocalID: "backend", Title: "backend", DependsOn: []string{"types"}, IdempotencyKey: "g1:backend"},
			{LocalID: "frontend", Title: "frontend", DependsOn: []string{"types"}, IdempotencyKey: "g1:frontend"},
			{LocalID: "e2e", Title: "integration", DependsOn: []string{"backend", "frontend"}, IdempotencyKey: "g1:e2e"},
		},
	}
}

// TestChaosFaultInjection is the Jepsen-style property test: across many seeded
// fault histories, the orchestrator must preserve every invariant. Whatever the
// faulty workers do — error, stall, conflict, fail verification, or propose
// cyclic/duplicate decompositions — main stays green, the log replays
// deterministically, no idempotency key merges twice, the graph stays a DAG, and
// every goal reaches a terminal state.
func TestChaosFaultInjection(t *testing.T) {
	requireGit(t)
	gate := chaosGate()
	allModes := []agent.FaultMode{
		agent.FaultError, agent.FaultNoChange, agent.FaultConflict,
		agent.FaultBadVerify, agent.FaultCyclic, agent.FaultDuplicate,
	}

	seeds := int64(40)
	if testing.Short() {
		seeds = 8
	}
	for s := int64(0); s < seeds; s++ {
		s := s
		t.Run(fmt.Sprintf("seed-%d", s), func(t *testing.T) {
			base := &agent.Mock{Decompose: diamondPlan()}
			faulty := agent.NewFaulty(base, s, allModes...)
			o, h := setup(t, faulty, gate, Options{
				Concurrency:       1 + int(s%4),
				MaxAttempts:       2,
				MaxGraphDepth:     4,
				MaxTicketsPerGoal: 32,
			})
			h.submitGoal(t, "g1", "build app")

			if err := o.Run(context.Background()); err != nil {
				t.Fatalf("Run did not converge (liveness): %v", err)
			}
			assertInvariants(t, h, gate)
		})
	}
}

// TestCrashRecoveryFromLedger simulates a process crash mid-work: a ticket was
// claimed and started but never proposed, with activity in the past. A fresh
// orchestrator (empty worktree map, state read purely from the durable log) must
// detect the stall, restart the ticket, and drive it to a verified merge — with
// all invariants intact. This is the crash-only recovery property.
func TestCrashRecoveryFromLedger(t *testing.T) {
	requireGit(t)
	gate := verify.Verifier{Commands: []verify.Command{{"true"}}}
	_, h := setup(t, agent.NewMock(), gate, Options{})

	past := time.Now().Add(-10 * time.Minute)
	h.appendAt(t, past, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "recover me"})
	h.appendAt(t, past, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g1-impl", GoalID: "g1", Title: "Implement: recover me", IdempotencyKey: "g1:impl"})
	h.appendAt(t, past, api.TicketReady, api.TicketReadyPayload{TicketID: "g1-impl"})
	h.appendAt(t, past, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g1-impl", Worker: "crashed"})
	h.appendAt(t, past, api.WorkStarted, api.WorkStartedPayload{TicketID: "g1-impl", Worker: "crashed", Worktree: "/gone"})

	// Fresh orchestrator: no in-memory worktrees, StallTimeout < the 10m gap.
	fresh := New(h.led, h.repo, agent.NewMock(), mergequeue.New(h.repo, gate),
		Options{Concurrency: 1, StallTimeout: time.Minute})
	if err := fresh.Run(context.Background()); err != nil {
		t.Fatalf("recovery run did not converge: %v", err)
	}

	s := h.state(t)
	if tk := s.Tickets["g1-impl"]; tk == nil || tk.Status != state.StatusMerged {
		t.Fatalf("ticket = %+v, want merged after recovery", s.Tickets["g1-impl"])
	}
	assertInvariants(t, h, gate)
}

// assertInvariants checks every pure invariant, that main is green, and that the
// run reached a terminal state.
func assertInvariants(t *testing.T, h *harness, gate verify.Verifier) {
	t.Helper()
	events, err := h.led.Read()
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	for _, v := range invariant.Check(events) {
		t.Errorf("invariant violation: %s", v)
	}
	for _, v := range invariant.MainGreen(context.Background(), gate, h.repo.Dir) {
		t.Errorf("main not green: %s", v)
	}
	for _, v := range invariant.Settled(events) {
		t.Errorf("not settled (liveness): %s", v)
	}
}
