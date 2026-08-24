package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/mergequeue"
	"github.com/bharadwaj6/ageOfAgents/internal/metrics"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
	"github.com/stretchr/testify/require"
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
	// Force-remove any worktrees the run leaves linked (preserved on terminal
	// failure, parked, or crashed) before t.TempDir's RemoveAll — otherwise their
	// admin files under <repo>/.git/worktrees race cleanup under parallel load.
	// Registered after t.TempDir so it runs first (LIFO).
	t.Cleanup(func() {
		out, err := exec.Command("git", "-C", repo.Dir, "worktree", "list", "--porcelain").Output()
		require.NoError(t, err)
		for _, line := range strings.Split(string(out), "\n") {
			if path, ok := strings.CutPrefix(line, "worktree "); ok {
				_ = exec.Command("git", "-C", repo.Dir, "worktree", "remove", "--force", path).Run()
			}
		}
		_ = exec.Command("git", "-C", repo.Dir, "worktree", "prune").Run()
	})
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
	ev, err := api.NewEvent(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: id, Text: text})
	require.NoError(t, err)
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
	events, err := h.led.Read()
	require.NoError(t, err)
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

func TestRunRejectsOverFanOutDecomposition(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	mock := &agent.Mock{
		Decompose: map[string][]agent.Subtask{
			"Implement: wide goal": {
				{LocalID: "a", Title: "a", IdempotencyKey: "g1:a"},
				{LocalID: "b", Title: "b", IdempotencyKey: "g1:b"},
				{LocalID: "c", Title: "c", IdempotencyKey: "g1:c"},
				{LocalID: "d", Title: "d", IdempotencyKey: "g1:d"},
			},
		},
	}
	// MaxFanOut 3 < the 4 children proposed: the whole batch is rejected.
	o, h := setup(t, mock, pass, Options{Concurrency: 2, MaxAttempts: 1, MaxFanOut: 3})
	h.submitGoal(t, "g1", "wide goal")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	s := h.state(t)

	root := s.Tickets["g1-impl"]
	if root == nil || root.Status != state.StatusFailed {
		t.Fatalf("root = %+v, want failed (over-fan-out decomposition rejected)", root)
	}
	if len(s.Tickets) != 1 {
		t.Errorf("no child tickets should have been created, got %d tickets", len(s.Tickets))
	}
}

func TestDetectStalled(t *testing.T) {
	now := time.Now()
	old := now.Add(-10 * time.Minute)

	claim, err := api.NewEvent(api.TicketClaimed, "o", api.TicketClaimedPayload{TicketID: "t1", Worker: "w"})
	require.NoError(t, err)
	claim.Seq = 3
	claim.Timestamp = old
	created, err := api.NewEvent(api.TicketCreated, "o", api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "k"})
	require.NoError(t, err)
	created.Seq = 1
	ready, err := api.NewEvent(api.TicketReady, "o", api.TicketReadyPayload{TicketID: "t1"})
	require.NoError(t, err)
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

// blockingBackend holds an agent run open until released, so a test can observe
// what the Scheduler does while a Worker is genuinely in flight.
type blockingBackend struct {
	started chan struct{}
	release chan struct{}
}

func (*blockingBackend) Name() string { return "blocking" }

func (b *blockingBackend) Run(ctx context.Context, task agent.Task) (agent.Result, error) {
	close(b.started)
	select {
	case <-b.release:
	case <-ctx.Done():
		return agent.Result{}, ctx.Err()
	}
	dst := filepath.Join(task.Worktree, "out.txt")
	if err := os.WriteFile(dst, []byte(task.Title+"\n"), 0o644); err != nil {
		return agent.Result{}, err
	}
	return agent.Result{Summary: task.Title}, nil
}

func TestHeartbeatEmittedDuringAgentRun(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	backend := &blockingBackend{started: make(chan struct{}), release: make(chan struct{})}
	o, h := setup(t, backend, pass, Options{Concurrency: 1, HeartbeatInterval: 10 * time.Millisecond})
	h.submitGoal(t, "g1", "slow work")

	done := make(chan error, 1)
	go func() { done <- o.Run(context.Background()) }()

	<-backend.started
	time.Sleep(80 * time.Millisecond) // long enough for several ticks
	close(backend.release)
	require.NoError(t, <-done)

	events, err := h.led.Read()
	require.NoError(t, err)
	beats := 0
	for _, e := range events {
		if e.Type != api.Heartbeat {
			continue
		}
		var p api.HeartbeatPayload
		require.NoError(t, e.DecodePayload(&p))
		if p.TicketID != "g1-impl" || p.Worker != "worker/g1-impl" {
			t.Errorf("heartbeat = %+v, want ticket g1-impl / worker/g1-impl", p)
		}
		beats++
	}
	if beats < 2 {
		t.Errorf("heartbeat count = %d, want at least 2 during an ~80ms run at 10ms", beats)
	}
	// The attempt still completes normally; heartbeats are observational.
	if tk := h.state(t).Tickets["g1-impl"]; tk == nil || tk.Status != state.StatusMerged {
		t.Fatalf("g1-impl status = %v, want merged", tk)
	}
}

func TestHeartbeatKeepsSlowWorkerFromStalling(t *testing.T) {
	// A long-running agent looks dead to the Stall Detector if the only liveness
	// signal is the dispatch timestamp. A Heartbeat advances LastActivity, which
	// is what lets a fresh process tell "slow" from "crashed".
	t0 := time.Now().Add(-10 * time.Minute)
	mk := func(seq int, ts time.Time, typ api.EventType, payload any) api.Event {
		e, err := api.NewEvent(typ, "o", payload)
		require.NoError(t, err)
		e.Seq, e.Timestamp = seq, ts
		return e
	}
	events := []api.Event{
		mk(1, t0, api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "k"}),
		mk(2, t0, api.TicketReady, api.TicketReadyPayload{TicketID: "t1"}),
		mk(3, t0, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "t1", Worker: "w"}),
	}
	now := t0.Add(5 * time.Minute)

	s, err := state.Fold(events)
	require.NoError(t, err)
	if got := detectStalled(s, now, 2*time.Minute); len(got) != 1 {
		t.Fatalf("without a heartbeat detectStalled = %v, want [t1]", got)
	}

	beating, err := state.Fold(append(events,
		mk(4, now.Add(-30*time.Second), api.Heartbeat, api.HeartbeatPayload{TicketID: "t1", Worker: "w"})))
	require.NoError(t, err)
	if got := detectStalled(beating, now, 2*time.Minute); len(got) != 0 {
		t.Errorf("a worker beating 30s ago was flagged stalled: %v", got)
	}
}

func TestSpendGovernorStopsOverBudgetGoal(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	mock := &agent.Mock{
		Decompose: map[string][]agent.Subtask{
			"Implement: budget goal": {
				{LocalID: "a", Title: "a", IdempotencyKey: "g1:a"},
				{LocalID: "b", Title: "b", IdempotencyKey: "g1:b"},
			},
		},
		TokensPerTask: 100, // the decomposition alone charges 100, over the 50 budget
	}
	o, h := setup(t, mock, pass, Options{Concurrency: 2, MaxTokensPerGoal: 50})
	h.submitGoal(t, "g1", "budget goal")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run did not converge: %v", err)
	}

	s := h.state(t)
	g := s.Goals["g1"]
	if !g.BudgetExceeded {
		t.Errorf("goal BudgetExceeded = false, want true (spent %d, limit 50)", g.TokensSpent)
	}
	for _, id := range []string{"g1-impl/a", "g1-impl/b"} {
		tk := s.Tickets[id]
		if tk == nil {
			t.Fatalf("child %s missing", id)
		}
		if tk.Status != state.StatusFailed {
			t.Errorf("child %s status = %s, want failed (budget)", id, tk.Status)
		}
	}
	// No child merged — the governor stopped further spend.
	for _, tk := range s.Tickets {
		if tk.Status == state.StatusMerged {
			t.Errorf("ticket %s merged, but the goal was over budget", tk.ID)
		}
	}
	// The trip is recorded exactly once (idempotent breaker).
	events, err := h.led.Read()
	require.NoError(t, err)
	trips := 0
	for _, e := range events {
		if e.Type == api.GoalBudgetExceeded {
			trips++
		}
	}
	if trips != 1 {
		t.Errorf("GoalBudgetExceeded count = %d, want 1", trips)
	}
}

// noChangeMock burns tokens on every attempt and never edits the worktree — the
// "agent produced no changes" path, which reaches no proposal and so charged the
// Goal nothing before failed attempts were accounted for.
func noChangeMock(tokens int) *agent.Mock {
	return &agent.Mock{Plan: map[string][]agent.File{"*": {}}, TokensPerTask: tokens}
}

func TestFailedAttemptsChargeGoalTokens(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, h := setup(t, noChangeMock(100), pass, Options{Concurrency: 1, MaxAttempts: 2})
	h.submitGoal(t, "g1", "burns tokens, ships nothing")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run did not converge: %v", err)
	}

	s := h.state(t)
	if tk := s.Tickets["g1-impl"]; tk == nil || tk.Status != state.StatusFailed {
		t.Fatalf("g1-impl status = %v, want failed", tk)
	}
	// Two attempts at 100 tokens each: the retry (WorkerRestarted) and the
	// terminal failure (TicketFailed) must both charge the Goal.
	if got := s.Goals["g1"].TokensSpent; got != 200 {
		t.Errorf("goal TokensSpent = %d, want 200 (2 failed attempts x 100)", got)
	}
	if got := s.Goals["g1"].TokensByModel["mock"]; got != 200 {
		t.Errorf("TokensByModel[mock] = %d, want 200", got)
	}
}

func TestSpendGovernorTripsOnFailedAttempts(t *testing.T) {
	// The failure spiral the governor exists to stop: work that burns budget
	// without ever producing a mergeable diff. The decomposition alone (100) is
	// under the 150 ceiling — only the failed child attempt pushes it over.
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	mock := noChangeMock(100)
	mock.Decompose = map[string][]agent.Subtask{
		"Implement: doomed goal": {
			{LocalID: "a", Title: "a", IdempotencyKey: "g1:a"},
			{LocalID: "b", Title: "b", IdempotencyKey: "g1:b"},
		},
	}
	o, h := setup(t, mock, pass, Options{Concurrency: 1, MaxAttempts: 1, MaxTokensPerGoal: 150})
	h.submitGoal(t, "g1", "doomed goal")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run did not converge: %v", err)
	}

	s := h.state(t)
	if !s.Goals["g1"].BudgetExceeded {
		t.Fatalf("goal BudgetExceeded = false, want true (spent %d, limit 150)", s.Goals["g1"].TokensSpent)
	}
	for _, id := range []string{"g1-impl/a", "g1-impl/b"} {
		if tk := s.Tickets[id]; tk == nil || tk.Status != state.StatusFailed {
			t.Errorf("child %s status = %v, want failed", id, tk)
		}
	}
	events, err := h.led.Read()
	require.NoError(t, err)
	trips := 0
	for _, e := range events {
		if e.Type == api.GoalBudgetExceeded {
			trips++
		}
	}
	if trips != 1 {
		t.Errorf("GoalBudgetExceeded count = %d, want 1", trips)
	}
}

func TestSpendGovernorRecordsUsdCeiling(t *testing.T) {
	// A cost trip must be distinguishable from a token trip on the log: it
	// carries LimitUSD/SpentUSD and leaves the token Limit zero.
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	// One failed attempt burns 1M tokens at $10/M, over the $5 ceiling. MaxAttempts=2
	// leaves the ticket Ready for a retry, so the governor gets a pass in which to
	// trip and stop it — the burn is what ends the Goal, not the attempt cap.
	o, h := setup(t, noChangeMock(1_000_000), pass, Options{
		Concurrency:   1,
		MaxAttempts:   2,
		MaxUsdPerGoal: 5,
		Pricing:       map[string]float64{"mock": 10}, // $10 per million tokens
	})
	h.submitGoal(t, "g1", "expensive goal")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run did not converge: %v", err)
	}

	events, err := h.led.Read()
	require.NoError(t, err)
	var trip api.GoalBudgetExceededPayload
	found := false
	for _, e := range events {
		if e.Type == api.GoalBudgetExceeded {
			require.NoError(t, e.DecodePayload(&trip))
			found = true
		}
	}
	if !found {
		t.Fatalf("no GoalBudgetExceeded event; goal spent %d tokens", h.state(t).Goals["g1"].TokensSpent)
	}
	if trip.LimitUSD != 5 {
		t.Errorf("LimitUSD = %v, want 5", trip.LimitUSD)
	}
	if trip.SpentUSD != 10 {
		t.Errorf("SpentUSD = %v, want 10 (1M tokens at $10/M)", trip.SpentUSD)
	}
	if trip.Limit != 0 {
		t.Errorf("Limit = %d, want 0 (this was a cost trip, not a token trip)", trip.Limit)
	}
}

func TestSpendGovernorOffByDefaultMergesGoal(t *testing.T) {
	// With MaxTokensPerGoal=0 the governor is inert: the goal completes normally
	// even though its work charges tokens.
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	mock := &agent.Mock{TokensPerTask: 10_000}
	o, h := setup(t, mock, pass, Options{Concurrency: 2})
	h.submitGoal(t, "g1", "unbudgeted")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if tk := h.state(t).Tickets["g1-impl"]; tk == nil || tk.Status != state.StatusMerged {
		t.Fatalf("g1-impl status = %v, want merged", tk)
	}
}

func TestBackoffFor(t *testing.T) {
	base := time.Second
	cases := []struct {
		attempts int
		want     time.Duration
	}{
		{0, 0}, {1, time.Second}, {2, 2 * time.Second}, {3, 4 * time.Second},
		{8, 64 * time.Second}, {20, 64 * time.Second}, // capped at base<<6
	}
	for _, c := range cases {
		if got := backoffFor(base, c.attempts); got != c.want {
			t.Errorf("backoffFor(1s, %d) = %v, want %v", c.attempts, got, c.want)
		}
	}
	if got := backoffFor(0, 3); got != 0 {
		t.Errorf("backoffFor(0, 3) = %v, want 0 (disabled)", got)
	}
}

func TestDispatchableRespectsBackoff(t *testing.T) {
	o := &Orchestrator{opt: Options{RetryBackoff: time.Minute}}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	fresh := &state.Ticket{ID: "fresh", Attempts: 0}
	retry := &state.Ticket{ID: "retry", Attempts: 1, LastActivity: t0}
	ready := []*state.Ticket{fresh, retry}

	// 30s after the failure: retry is still in its 1m backoff; fresh dispatches.
	got := o.dispatchable(ready, t0.Add(30*time.Second))
	if len(got) != 1 || got[0].ID != "fresh" {
		t.Errorf("during backoff: got %d tickets, want only [fresh]", len(got))
	}
	// Past the backoff: both dispatch.
	if got := o.dispatchable(ready, t0.Add(61*time.Second)); len(got) != 2 {
		t.Errorf("after backoff: got %d, want 2", len(got))
	}
	// Backoff off: no filtering.
	off := &Orchestrator{opt: Options{}}
	if got := off.dispatchable(ready, t0); len(got) != 2 {
		t.Errorf("backoff disabled: got %d, want 2", len(got))
	}
}

func TestBackoffDelaysRedispatch(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := t0
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, h := setup(t, agent.NewMock(), pass, Options{
		Concurrency:  1,
		RetryBackoff: time.Minute,
		Now:          func() time.Time { return clock },
	})
	// Seed a ticket that just failed verification at t0 (Ready, Attempts=1).
	h.submitGoal(t, "g1", "x")
	h.appendAt(t, t0, api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "impl", IdempotencyKey: "g1:impl"})
	h.appendAt(t, t0, api.TicketReady, api.TicketReadyPayload{TicketID: "t1"})
	h.appendAt(t, t0, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "t1", Worker: "w"})
	h.appendAt(t, t0, api.VerificationFailed, api.VerificationFailedPayload{TicketID: "t1", Worker: "w", Reason: "boom"})

	// Within the 1m backoff window: ReconcileOnce must not dispatch.
	clock = t0.Add(30 * time.Second)
	before := h.state(t).LastSeq
	if err := o.ReconcileOnce(context.Background()); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}
	if got := h.state(t).LastSeq; got != before {
		t.Errorf("dispatched during backoff (seq %d→%d)", before, got)
	}
	if st := h.state(t).Tickets["t1"].Status; st != state.StatusReady {
		t.Errorf("status = %s, want ready (still backing off)", st)
	}

	// Past the backoff: it dispatches and merges (gate passes).
	clock = t0.Add(2 * time.Minute)
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run after backoff: %v", err)
	}
	if st := h.state(t).Tickets["t1"].Status; st != state.StatusMerged {
		t.Errorf("status = %s, want merged after backoff elapsed", st)
	}
}

func TestCrashLoopGivesUpEarly(t *testing.T) {
	fail := verify.Verifier{Commands: []verify.Command{{"false"}}} // always fails the gate
	o, h := setup(t, agent.NewMock(), fail, Options{
		Concurrency:        1,
		MaxAttempts:        5,
		CrashLoopThreshold: 3,
	})
	h.submitGoal(t, "g1", "loops")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run did not converge: %v", err)
	}

	tk := h.state(t).Tickets["g1-impl"]
	if tk == nil || tk.Status != state.StatusFailed {
		t.Fatalf("g1-impl status = %v, want failed", tk)
	}
	if tk.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3 (crash loop tripped before MaxAttempts=5)", tk.Attempts)
	}

	events, err := h.led.Read()
	require.NoError(t, err)
	verifFails := 0
	failReason := ""
	for _, e := range events {
		switch e.Type {
		case api.VerificationFailed:
			verifFails++
		case api.TicketFailed:
			var p api.TicketFailedPayload
			_ = e.DecodePayload(&p)
			failReason = p.Reason
		}
	}
	if verifFails != 2 {
		t.Errorf("VerificationFailed count = %d, want 2 (3rd identical failure is the crash-loop terminal)", verifFails)
	}
	if !strings.Contains(failReason, "crash loop") {
		t.Errorf("terminal reason = %q, want it to mention a crash loop", failReason)
	}
}

// recordBackend captures the Task.Goal text each Run sees, then delegates.
type recordBackend struct {
	inner agent.Backend
	mu    sync.Mutex
	goals []string
}

func (r *recordBackend) Name() string { return "record" }
func (r *recordBackend) Run(ctx context.Context, task agent.Task) (agent.Result, error) {
	r.mu.Lock()
	r.goals = append(r.goals, task.Goal)
	r.mu.Unlock()
	return r.inner.Run(ctx, task)
}
func (r *recordBackend) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.goals...)
}

// taskRecorder captures every Task it is handed, then delegates.
type taskRecorder struct {
	inner agent.Backend
	mu    sync.Mutex
	tasks []agent.Task
}

func (r *taskRecorder) Name() string { return "task-recorder" }
func (r *taskRecorder) Run(ctx context.Context, task agent.Task) (agent.Result, error) {
	r.mu.Lock()
	r.tasks = append(r.tasks, task)
	r.mu.Unlock()
	return r.inner.Run(ctx, task)
}
func (r *taskRecorder) seen() []agent.Task {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agent.Task(nil), r.tasks...)
}

func TestVerificationFailureFeedsNextAttempt(t *testing.T) {
	// A gate that fails its first run (emitting a recognizable marker) and passes
	// thereafter, so the second attempt should carry the first attempt's output.
	sentinel := filepath.Join(t.TempDir(), "gate-ran")
	script := "if [ -f " + sentinel + " ]; then exit 0; fi; touch " + sentinel +
		"; echo GATE_FAIL_MARKER: build broken; exit 1"
	gate := verify.Verifier{Commands: []verify.Command{{"sh", "-c", script}}}
	rec := &taskRecorder{inner: agent.NewMock()}
	o, h := setup(t, rec, gate, Options{Concurrency: 1, MaxAttempts: 3})
	h.submitGoal(t, "g1", "build app")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if tk := h.state(t).Tickets["g1-impl"]; tk == nil || tk.Status != state.StatusMerged {
		t.Fatalf("g1-impl = %+v, want merged after the gate passes on retry", tk)
	}

	tasks := rec.seen()
	if len(tasks) != 2 {
		t.Fatalf("backend saw %d tasks, want 2 (fail then retry)", len(tasks))
	}
	if tasks[0].Attempt != 1 || tasks[0].LastFailure != "" {
		t.Errorf("first task = {Attempt:%d, LastFailure:%q}, want attempt 1 with no prior failure", tasks[0].Attempt, tasks[0].LastFailure)
	}
	if tasks[1].Attempt != 2 {
		t.Errorf("retry Attempt = %d, want 2", tasks[1].Attempt)
	}
	if !strings.Contains(tasks[1].LastFailure, "GATE_FAIL_MARKER") {
		t.Errorf("retry LastFailure = %q, want it to carry the prior gate output", tasks[1].LastFailure)
	}
}

func TestDependencyContext(t *testing.T) {
	s := state.New()
	s.Tickets["dep1"] = &state.Ticket{ID: "dep1", Title: "define types", Summary: "added User and Order structs", Commit: "abcdef0123456789", Status: state.StatusMerged}
	s.Tickets["dep2"] = &state.Ticket{ID: "dep2", Title: "work in progress", Status: state.StatusRunning}

	got := dependencyContext(s, &state.Ticket{ID: "t", DependsOn: []string{"dep1", "dep2", "missing"}})
	if !strings.Contains(got, "define types: added User and Order structs") {
		t.Errorf("merged dependency summary missing:\n%s", got)
	}
	if !strings.Contains(got, "abcdef012345") || strings.Contains(got, "abcdef0123456789") {
		t.Errorf("commit should be abbreviated to 12 chars:\n%s", got)
	}
	if strings.Contains(got, "work in progress") {
		t.Errorf("non-merged dependency should be omitted:\n%s", got)
	}
	if c := dependencyContext(s, &state.Ticket{ID: "root"}); c != "" {
		t.Errorf("a ticket with no dependencies should get empty context, got %q", c)
	}
}

func TestDependencyContextReachesDependent(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	mock := &agent.Mock{
		Decompose: map[string][]agent.Subtask{
			"Implement: build app": {
				{LocalID: "types", Title: "define shared User type", IdempotencyKey: "g1:types"},
				{LocalID: "api", Title: "implement the API", DependsOn: []string{"types"}, IdempotencyKey: "g1:api"},
			},
		},
	}
	rec := &taskRecorder{inner: mock}
	o, h := setup(t, rec, pass, Options{Concurrency: 4})
	h.submitGoal(t, "g1", "build app")
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, id := range []string{"g1-impl/types", "g1-impl/api"} {
		if tk := h.state(t).Tickets[id]; tk == nil || tk.Status != state.StatusMerged {
			t.Fatalf("child %s = %+v, want merged", id, tk)
		}
	}

	var apiCtx string
	found := false
	for _, task := range rec.seen() {
		if task.Title == "implement the API" {
			apiCtx, found = task.DepContext, true
		}
	}
	if !found {
		t.Fatal("no dispatch recorded for the dependent (API) child")
	}
	if !strings.Contains(apiCtx, "define shared User type") {
		t.Errorf("dependent's DepContext = %q, want it to carry the merged dependency from the Shared Log", apiCtx)
	}
}

func TestGoalAmendmentReachesNextDispatch(t *testing.T) {
	gate := verify.Verifier{Commands: []verify.Command{{"true"}}}
	rec := &recordBackend{inner: agent.NewMock()}
	o, h := setup(t, rec, gate, Options{Concurrency: 1})
	h.submitGoal(t, "g1", "build a parser")

	// Amend before the run: the dispatch should carry the amended context.
	h.appendAt(t, time.Now(), api.GoalAmended, api.GoalAmendedPayload{GoalID: "g1", Guidance: "ALSO_HANDLE_COMMENTS"})

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	seen := rec.seen()
	if len(seen) == 0 {
		t.Fatal("backend was never dispatched")
	}
	for _, g := range seen {
		if !strings.Contains(g, "build a parser") || !strings.Contains(g, "ALSO_HANDLE_COMMENTS") {
			t.Errorf("dispatched goal text = %q, want it to include the original + the amendment", g)
		}
	}
}

func TestBatchMergePathEngagesAndStaysCorrect(t *testing.T) {
	// A diamond's backend+frontend leaves (disjoint marker files) propose in the
	// same pass, so the merge queue's disjoint-batch path runs. Assert everything
	// merged and the queue actually held >1 proposal at once (batching engaged).
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	mock := &agent.Mock{
		Decompose: map[string][]agent.Subtask{
			"Implement: build app": {
				{LocalID: "types", Title: "shared types", IdempotencyKey: "g1:types"},
				{LocalID: "backend", Title: "backend", DependsOn: []string{"types"}, IdempotencyKey: "g1:backend"},
				{LocalID: "frontend", Title: "frontend", DependsOn: []string{"types"}, IdempotencyKey: "g1:frontend"},
			},
		},
	}
	o, h := setup(t, mock, pass, Options{Concurrency: 4})
	h.submitGoal(t, "g1", "build app")
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	s := h.state(t)
	for _, id := range []string{"g1-impl/types", "g1-impl/backend", "g1-impl/frontend"} {
		if tk := s.Tickets[id]; tk == nil || tk.Status != state.StatusMerged {
			t.Fatalf("child %s = %+v, want merged", id, tk)
		}
	}
	events, err := h.led.Read()
	require.NoError(t, err)
	if d := metrics.Compute(events).MergeQueueMaxDepth; d < 2 {
		t.Errorf("MergeQueueMaxDepth = %d, want >= 2 (backend+frontend queue together)", d)
	}
	assertInvariants(t, h, pass)
}

func TestTerminalFailurePreservesWorktreeForHandoff(t *testing.T) {
	gate := verify.Verifier{Commands: []verify.Command{{"true"}}}
	mock := &agent.Mock{FailTitles: map[string]bool{"Implement: doomed": true}}
	wtBase := t.TempDir()
	o, h := setup(t, mock, gate, Options{Concurrency: 1, MaxAttempts: 2, WorktreeBase: wtBase})
	h.submitGoal(t, "g1", "doomed")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	tk := h.state(t).Tickets["g1-impl"]
	if tk == nil || tk.Status != state.StatusFailed {
		t.Fatalf("status = %v, want failed", tk)
	}
	// A terminal failure preserves the agent's last attempt for a warm handoff.
	if tk.Worktree == "" {
		t.Fatal("terminal failure should preserve and record the worktree path")
	}
	if _, err := os.Stat(tk.Worktree); err != nil {
		t.Errorf("preserved worktree should remain on disk: %v", err)
	}
	if tk.LastFailReason == "" {
		t.Error("failed ticket should record why it gave up")
	}
	// Only the final attempt's worktree remains; the earlier retry was cleaned up.
	entries, err := os.ReadDir(wtBase)
	if err != nil {
		t.Fatalf("read worktree base: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected exactly 1 preserved worktree, got %d (retries should clean up)", len(entries))
	}
}

func TestRegressionEscapeOnCoupledMultiFileChange(t *testing.T) {
	// A deliberately-coupled change: ticket A introduces a new API in api.go;
	// ticket B (textually disjoint, in caller.go, depending on A) uses the OLD
	// API. The two merge cleanly — no textual conflict — and the narrow Gate
	// (`true`) accepts both, but a broader Shadow set catches the stale usage.
	// This is the verification blind spot the regression-escape rate measures.
	mock := &agent.Mock{
		Decompose: map[string][]agent.Subtask{
			"Implement: coupled goal": {
				{LocalID: "a", Title: "write api", IdempotencyKey: "g1:a"},
				{LocalID: "b", Title: "call api", DependsOn: []string{"a"}, IdempotencyKey: "g1:b"},
			},
		},
		Plan: map[string][]agent.File{
			"write api": {{Path: "api.go", Content: "// API v2\n"}},
			"call api":  {{Path: "caller.go", Content: "// USES_OLD_API\n"}},
		},
	}
	narrowGate := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, h := setup(t, mock, narrowGate, Options{Concurrency: 2})
	// Broader set: fail if any tracked file still uses the old API.
	o.mq.Shadow = verify.Verifier{Commands: []verify.Command{
		{"sh", "-c", "! grep -rqF --exclude-dir=.git USES_OLD_API ."},
	}}
	h.submitGoal(t, "g1", "coupled goal")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Both children merged cleanly under the narrow Gate (no textual conflict).
	for _, id := range []string{"g1-impl/a", "g1-impl/b"} {
		if tk := h.state(t).Tickets[id]; tk == nil || tk.Status != state.StatusMerged {
			t.Fatalf("%s status = %v, want merged", id, tk)
		}
	}
	// But the broader Shadow set caught the stale-API regression the Gate missed.
	events, err := h.led.Read()
	require.NoError(t, err)
	m := metrics.Compute(events)
	if m.RegressionEscapes < 1 || m.RegressionEscapeRate == 0 {
		t.Errorf("expected a regression escape; got escapes=%d rate=%v", m.RegressionEscapes, m.RegressionEscapeRate)
	}
}

// A wedged agent must not hang the run. Before AgentTimeout existed there was no
// context deadline anywhere between `aoa run` and exec.CommandContext, so a CLI
// that never returned blocked the dispatch wave forever — and the Stall Detector
// could not help, because it only runs after that wave joins.
func TestAgentTimeoutCancelsAHungAttempt(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	// release is never closed: this backend blocks until its context is done.
	backend := &blockingBackend{started: make(chan struct{}), release: make(chan struct{})}
	o, h := setup(t, backend, pass, Options{
		Concurrency:  1,
		MaxAttempts:  1,
		AgentTimeout: 50 * time.Millisecond,
	})
	h.submitGoal(t, "g1", "hangs forever")

	done := make(chan error, 1)
	go func() { done <- o.Run(context.Background()) }()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Run hung — the agent attempt was never bounded")
	}

	tk := h.state(t).Tickets["g1-impl"]
	require.NotNil(t, tk)
	require.Equal(t, state.StatusFailed, tk.Status, "a timed-out attempt must fail the ticket")
	require.Contains(t, tk.LastFailReason, "timed out", "the reason should name the timeout, got %q", tk.LastFailReason)
}

// The commits aoa writes are its actual output and land in the user's history.
// A Goal becomes a ticket titled "Implement: <whole goal text>", so the old
// `fmt.Sprintf("feat: %s (%s)", title, id)` produced a subject line that was an
// entire paragraph plus a ticket id.
func TestCommitMessage(t *testing.T) {
	longGoal := "Implement: Add table-driven unit tests for parseUsage in " +
		"internal/agent/claudecode.go. It currently has zero test coverage. " +
		"Do not change any non-test code."

	for _, tc := range []struct {
		name, title, ticket string
		wantSubject         string
	}{
		{
			name:        "short title is left alone",
			title:       "Implement: add a greeting function",
			ticket:      "g-1-impl",
			wantSubject: "feat: add a greeting function",
		},
		{
			name:        "untitled ticket falls back to its id",
			title:       "Implement: ",
			ticket:      "g-3-impl",
			wantSubject: "feat: g-3-impl",
		},
		{
			name:   "long title is cut at a word boundary",
			title:  longGoal,
			ticket: "g-2-impl",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := commitMessage(tc.title, tc.ticket)
			subject, body, found := strings.Cut(msg, "\n")

			if len([]rune(subject)) > subjectMax {
				t.Errorf("subject is %d runes, over the %d cap:\n%s", len([]rune(subject)), subjectMax, subject)
			}
			if strings.HasPrefix(subject, "feat: Implement:") {
				t.Errorf("the Implement: prefix should be stripped, got %q", subject)
			}
			if tc.wantSubject != "" && subject != tc.wantSubject {
				t.Errorf("subject = %q, want %q", subject, tc.wantSubject)
			}
			// The full task text survives in the body, so nothing is lost.
			if !found || !strings.Contains(body, strings.TrimSpace(tc.title)) {
				t.Errorf("body should carry the full title, got %q", body)
			}
			if !strings.Contains(body, tc.ticket) {
				t.Errorf("body should name the ticket %q, got %q", tc.ticket, body)
			}
		})
	}
}

// An abandoned attempt used to record nothing about why it failed: the reason
// reached failAttempt and was dropped on the floor by WorkerRestarted. So the
// retry re-ran an identical prompt, and crash-loop detection — which keys on
// repeated identical reasons — could never fire on anything but a Gate
// rejection, letting an agent that fails the same way every time burn the whole
// attempt budget.
func TestRestartedAttemptCarriesItsReasonForward(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	mock := &agent.Mock{FailTitles: map[string]bool{"Implement: explode": true}}
	o, h := setup(t, mock, pass, Options{Concurrency: 1, MaxAttempts: 3})
	h.submitGoal(t, "g1", "explode")

	require.NoError(t, o.Run(context.Background()))

	// The reason reaches the log.
	events, err := h.led.Read()
	require.NoError(t, err)
	var restarts int
	for _, e := range events {
		if e.Type != api.WorkerRestarted {
			continue
		}
		restarts++
		var p api.WorkerRestartedPayload
		require.NoError(t, e.DecodePayload(&p))
		if p.Reason == "" {
			t.Error("WorkerRestarted recorded no reason for abandoning the attempt")
		}
	}
	if restarts == 0 {
		t.Fatal("expected at least one restarted attempt")
	}

	// And onto the ticket, so the retry prompt and the crash-loop breaker see it.
	tk := h.state(t).Tickets["g1-impl"]
	require.NotNil(t, tk)
	if tk.LastFailReason == "" {
		t.Error("ticket carries no LastFailReason after an abandoned attempt")
	}
	if tk.SameFailCount < 2 {
		t.Errorf("SameFailCount = %d; identical repeated failures must accumulate", tk.SameFailCount)
	}
}

// A ledger append failure inside decompose used to vanish: the bare `return` on
// loadState, and three `_ = o.emit(...)`, meant the agent's tokens were spent,
// its decomposition was dropped, and the ticket stayed claimed-and-running
// forever — recoverable only by the Stall Detector. The recordDispatchErr
// machinery existed by then; it just hadn't been applied here.
func TestDecomposeSurfacesLedgerFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permissions would not deny the write")
	}
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, h := setup(t, agent.NewMock(), pass, Options{Concurrency: 1})
	h.submitGoal(t, "g1", "split me")

	// A readable-but-unwritable ledger: Read still folds state, Append fails.
	ledgerPath := filepath.Join(h.base, "events.jsonl")
	require.NoError(t, os.Chmod(ledgerPath, 0o444))
	t.Cleanup(func() { _ = os.Chmod(ledgerPath, 0o644) })

	o.decompose(
		dispatchJob{ticketID: "g1-impl", goalID: "g1", title: "t"},
		"worker/g1-impl",
		[]agent.Subtask{{LocalID: "a", Title: "first"}},
		100, "m",
	)

	if err := o.takeDispatchErr(); err == nil {
		t.Fatal("decompose swallowed a ledger append failure")
	}
}

// Same for the rejection path: a parent whose decomposition is refused must not
// be left running because the TicketFailed append was dropped.
func TestFailDecomposeSurfacesLedgerFailures(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file permissions would not deny the write")
	}
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, h := setup(t, agent.NewMock(), pass, Options{Concurrency: 1})
	h.submitGoal(t, "g1", "split me")

	ledgerPath := filepath.Join(h.base, "events.jsonl")
	require.NoError(t, os.Chmod(ledgerPath, 0o444))
	t.Cleanup(func() { _ = os.Chmod(ledgerPath, 0o644) })

	o.failDecompose(dispatchJob{ticketID: "g1-impl", goalID: "g1"}, "worker/g1-impl", "would cycle")

	if err := o.takeDispatchErr(); err == nil {
		t.Fatal("failDecompose swallowed a ledger append failure")
	}
}

// slowThenFastBackend blocks the first task it sees until released, and returns
// immediately for every other task.
type slowThenFastBackend struct {
	mu       sync.Mutex
	slowID   string
	release  chan struct{}
	slowSeen chan struct{}
	once     sync.Once
}

func (*slowThenFastBackend) Name() string { return "slow-then-fast" }

func (b *slowThenFastBackend) Run(ctx context.Context, task agent.Task) (agent.Result, error) {
	b.mu.Lock()
	if b.slowID == "" {
		b.slowID = task.TicketID
	}
	isSlow := b.slowID == task.TicketID
	b.mu.Unlock()

	if isSlow {
		b.once.Do(func() { close(b.slowSeen) })
		select {
		case <-b.release:
		case <-ctx.Done():
			return agent.Result{}, ctx.Err()
		}
	}
	dst := filepath.Join(task.Worktree, worktree.SanitizeBranch(task.TicketID)+".txt")
	if err := os.WriteFile(dst, []byte(task.Title+"\n"), 0o644); err != nil {
		return agent.Result{}, err
	}
	return agent.Result{Summary: task.Title}, nil
}

// A finished proposal must reach the merge queue while a slow sibling is still
// running. Dispatch used to be a wave — ReconcileOnce launched every worker,
// blocked on wg.Wait(), and only then drained the queue — so one straggler held
// up every merge in its batch for up to AgentTimeout (ADR 013).
func TestFastTicketMergesWhileSlowSiblingRuns(t *testing.T) {
	backend := &slowThenFastBackend{release: make(chan struct{}), slowSeen: make(chan struct{})}
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, h := setup(t, backend, pass, Options{Concurrency: 2, PollInterval: time.Millisecond})

	// Two independent goals, so both are ready at once and share a dispatch pass.
	h.submitGoal(t, "slow", "the slow one")
	h.submitGoal(t, "fast", "the fast one")

	done := make(chan error, 1)
	go func() { done <- o.Run(context.Background()) }()

	// Wait until the slow worker is definitely blocked inside the backend.
	select {
	case <-backend.slowSeen:
	case <-time.After(30 * time.Second):
		t.Fatal("the slow worker never started")
	}

	// While it is still blocked, the other ticket must get all the way to merged.
	deadline := time.After(30 * time.Second)
	for {
		slowID := backend.slowID
		var other string
		for id := range h.state(t).Tickets {
			if id != slowID {
				other = id
			}
		}
		if other != "" {
			if tk := h.state(t).Tickets[other]; tk != nil && tk.Status == state.StatusMerged {
				break // the point of the test
			}
		}
		select {
		case <-deadline:
			t.Fatal("the fast ticket never merged while its slow sibling ran — dispatch is still a wave")
		case err := <-done:
			t.Fatalf("Run returned early (%v) before the fast ticket merged", err)
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	close(backend.release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not finish after the slow worker was released")
	}
}

// slowBackend blocks until released, standing in for a real coding agent that
// thinks for minutes rather than milliseconds.
type slowBackend struct {
	inner    agent.Backend
	once     sync.Once
	released chan struct{}
}

// release unblocks Run. Safe to call from both the poll counter and the
// backstop timer.
func (s *slowBackend) release() { s.once.Do(func() { close(s.released) }) }

func (s *slowBackend) Name() string { return "slow" }

func (s *slowBackend) Run(ctx context.Context, task agent.Task) (agent.Result, error) {
	select {
	case <-s.released:
	case <-ctx.Done():
		return agent.Result{}, ctx.Err()
	}
	return s.inner.Run(ctx, task)
}

// max_passes is documented as a safety bound against a livelock — a run that
// keeps emitting events without converging. It was also, accidentally, a
// wall-clock timeout: a pass spent purely *waiting* for an in-flight worker
// consumed the budget, so the default 1000 passes x 100ms poll_interval capped a
// run at ~100 seconds of agent time. Every real backend exceeds that; a live
// codex run took 183s and died with "orchestrator exceeded 1000 passes" holding
// a perfectly good proposal.
func TestWaitingForAWorkerDoesNotConsumeThePassBudget(t *testing.T) {
	slow := &slowBackend{inner: agent.NewMock(), released: make(chan struct{})}
	// Backstop: if the budget is consumed by waiting, Run returns early, the
	// poll count never reaches its target, and the dispatch goroutine would
	// block forever. Release on a timer too, so a regression fails rather
	// than hangs.
	timer := time.AfterFunc(10*time.Second, func() { slow.release() })
	defer timer.Stop()

	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	// A budget far below the number of polls this run needs.
	o, h := setup(t, slow, pass, Options{Concurrency: 1, MaxPasses: 5})

	// The injected Sleep does not actually sleep. That keeps the loop spinning
	// at CPU speed so almost no wall-clock time elapses, which in turn keeps
	// the worker's heartbeats — real events, and therefore genuinely productive
	// passes — from making this test a race between two clocks. It was exactly
	// that race that made an earlier version of this test flaky under -race.
	var mu sync.Mutex
	var polls int
	o.opt.Sleep = func(time.Duration) {
		mu.Lock()
		polls++
		n := polls
		mu.Unlock()
		if n == 200 {
			slow.release()
		}
	}
	h.submitGoal(t, "g1", "add greeting")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	got := polls
	mu.Unlock()
	if got < 200 {
		t.Errorf("run stopped after %d polls; waiting is still consuming the %d-pass budget", got, 5)
	}
	if tk := h.state(t).Tickets["g1-impl"]; tk == nil || tk.Status != state.StatusMerged {
		t.Fatalf("ticket = %+v, want merged", tk)
	}
}

// The bound must still hold against real churn: a run that keeps making progress
// without ever settling has to stop, or `aoa run` never returns.
func TestMaxPassesStillBoundsProductivePasses(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, h := setup(t, agent.NewMock(), pass, Options{Concurrency: 1, MaxPasses: 1})
	h.submitGoal(t, "g1", "add greeting")
	h.submitGoal(t, "g2", "add farewell")

	err := o.Run(context.Background())
	if err == nil {
		t.Fatal("a one-pass budget must not complete two goals — the bound is gone")
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("error should name the pass bound, got %v", err)
	}
}
