package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/mergequeue"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

type harness struct {
	led  *ledger.Ledger
	repo *worktree.Repo
	base string
}

func setup(t *testing.T, backend agent.Backend, gate verify.Verifier, opt Options) (*Orchestrator, *harness) {
	t.Helper()
	requireGit(t)
	base := t.TempDir()
	repo, err := worktree.InitRepo(context.Background(), filepath.Join(base, "repo"))
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	led, err := ledger.Open(filepath.Join(base, "events.jsonl"))
	if err != nil {
		t.Fatalf("ledger.Open: %v", err)
	}
	if opt.WorktreeBase == "" {
		opt.WorktreeBase = filepath.Join(base, "wt")
	}
	o := New(led, repo, backend, mergequeue.New(repo, gate), opt)
	return o, &harness{led: led, repo: repo, base: base}
}

func (h *harness) submitGoal(t *testing.T, id, text string) {
	t.Helper()
	ev, _ := api.NewEvent(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: id, Text: text})
	if _, err := h.led.Append(ev); err != nil {
		t.Fatalf("submit goal: %v", err)
	}
}

// appendAt appends an event with an explicit timestamp (used to simulate
// activity in the past, e.g. a crashed worker the Stall Detector should flag).
func (h *harness) appendAt(t *testing.T, ts time.Time, typ api.EventType, payload any) {
	t.Helper()
	ev, err := api.NewEvent(typ, "test", payload)
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	ev.Timestamp = ts
	if _, err := h.led.Append(ev); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func (h *harness) state(t *testing.T) *state.State {
	t.Helper()
	events, _ := h.led.Read()
	s, err := state.Fold(events)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	return s
}

func TestRunMergesGoalEndToEnd(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, h := setup(t, agent.NewMock(), pass, Options{Concurrency: 2})
	h.submitGoal(t, "g1", "add greeting")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := h.state(t)
	tk := s.Tickets["g1-impl"]
	if tk == nil || tk.Status != state.StatusMerged {
		t.Fatalf("ticket = %+v, want merged", tk)
	}
	if !s.Settled() {
		t.Error("expected settled")
	}
	// The mock's default marker file should now be on main.
	if _, err := os.Stat(filepath.Join(h.repo.Dir, "g1-impl.txt")); err != nil {
		t.Errorf("merged file should be on main: %v", err)
	}
}

func TestRunFailsTicketAfterMaxAttempts(t *testing.T) {
	fail := verify.Verifier{Commands: []verify.Command{{"false"}}}
	o, h := setup(t, agent.NewMock(), fail, Options{Concurrency: 1, MaxAttempts: 2})
	h.submitGoal(t, "g1", "doomed work")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := h.state(t)
	tk := s.Tickets["g1-impl"]
	if tk == nil || tk.Status != state.StatusFailed {
		t.Fatalf("ticket = %+v, want failed", tk)
	}
	if tk.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", tk.Attempts)
	}
	// Nothing should have landed on main.
	if _, err := os.Stat(filepath.Join(h.repo.Dir, "g1-impl.txt")); !os.IsNotExist(err) {
		t.Error("failed work must not be on main")
	}
}

func TestRunFailsWhenAgentErrors(t *testing.T) {
	mock := &agent.Mock{FailTitles: map[string]bool{"Implement: explode": true}}
	o, h := setup(t, mock, verify.Verifier{Commands: []verify.Command{{"true"}}}, Options{Concurrency: 1, MaxAttempts: 2})
	h.submitGoal(t, "g1", "explode")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	tk := h.state(t).Tickets["g1-impl"]
	if tk == nil || tk.Status != state.StatusFailed {
		t.Fatalf("ticket = %+v, want failed (agent errored every attempt)", tk)
	}
}

func TestRunEmergentDiamondDecomposition(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	mock := &agent.Mock{
		// The root ticket's title is "Implement: <goal text>"; the worker
		// decomposes it into a diamond instead of editing code. Each child has
		// no Decompose/Plan entry, so the mock writes a marker file (a real diff).
		Decompose: map[string][]agent.Subtask{
			"Implement: build chat app": {
				{LocalID: "types", Title: "shared types", IdempotencyKey: "g1:types"},
				{LocalID: "backend", Title: "backend", DependsOn: []string{"types"}, IdempotencyKey: "g1:backend"},
				{LocalID: "frontend", Title: "frontend", DependsOn: []string{"types"}, IdempotencyKey: "g1:frontend"},
				{LocalID: "e2e", Title: "integration", DependsOn: []string{"backend", "frontend"}, IdempotencyKey: "g1:e2e"},
			},
		},
	}
	o, h := setup(t, mock, pass, Options{Concurrency: 4})
	h.submitGoal(t, "g1", "build chat app")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	s := h.state(t)

	root := s.Tickets["g1-impl"]
	if root == nil || root.Status != state.StatusDecomposed {
		t.Fatalf("root = %+v, want decomposed", root)
	}
	children := []string{"g1-impl/types", "g1-impl/backend", "g1-impl/frontend", "g1-impl/e2e"}
	for _, id := range children {
		tk := s.Tickets[id]
		if tk == nil || tk.Status != state.StatusMerged {
			t.Fatalf("child %s = %+v, want merged", id, tk)
		}
		if tk.Depth != 1 {
			t.Errorf("child %s depth = %d, want 1", id, tk.Depth)
		}
	}
	if !s.Settled() {
		t.Error("expected settled")
	}
	for _, f := range []string{"g1-impl/types.txt", "g1-impl/backend.txt", "g1-impl/frontend.txt", "g1-impl/e2e.txt"} {
		if _, err := os.Stat(filepath.Join(h.repo.Dir, f)); err != nil {
			t.Errorf("merged file %s should be on main: %v", f, err)
		}
	}
}

func TestRunRejectsCyclicDecomposition(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	mock := &agent.Mock{
		Decompose: map[string][]agent.Subtask{
			"Implement: cyclic goal": {
				{LocalID: "a", Title: "a", DependsOn: []string{"b"}, IdempotencyKey: "g1:a"},
				{LocalID: "b", Title: "b", DependsOn: []string{"a"}, IdempotencyKey: "g1:b"},
			},
		},
	}
	o, h := setup(t, mock, pass, Options{Concurrency: 2, MaxAttempts: 1})
	h.submitGoal(t, "g1", "cyclic goal")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	s := h.state(t)

	root := s.Tickets["g1-impl"]
	if root == nil || root.Status != state.StatusFailed {
		t.Fatalf("root = %+v, want failed (cyclic decomposition rejected)", root)
	}
	if len(s.Tickets) != 1 {
		t.Errorf("no child tickets should have been created, got %d tickets", len(s.Tickets))
	}
}

func TestDetectStalled(t *testing.T) {
	now := time.Now()
	old := now.Add(-10 * time.Minute)

	claim, _ := api.NewEvent(api.TicketClaimed, "o", api.TicketClaimedPayload{TicketID: "t1", Worker: "w"})
	claim.Seq = 3
	claim.Timestamp = old
	created, _ := api.NewEvent(api.TicketCreated, "o", api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "k"})
	created.Seq = 1
	ready, _ := api.NewEvent(api.TicketReady, "o", api.TicketReadyPayload{TicketID: "t1"})
	ready.Seq = 2

	s, err := state.Fold([]api.Event{created, ready, claim})
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}

	stalled := detectStalled(s, now, 2*time.Minute)
	if len(stalled) != 1 || stalled[0].ID != "t1" {
		t.Fatalf("detectStalled = %v, want [t1]", stalled)
	}
	// A recent claim should not be flagged.
	if got := detectStalled(s, old.Add(time.Minute), 2*time.Minute); len(got) != 0 {
		t.Errorf("recent activity should not be stalled, got %v", got)
	}
}
