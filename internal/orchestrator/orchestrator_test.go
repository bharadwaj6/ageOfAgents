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
		out, _ := exec.Command("git", "-C", repo.Dir, "worktree", "list", "--porcelain").Output()
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
	events, _ := h.led.Read()
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
	h.appendAt(t, t0, api.VerificationFailed, api.VerificationFailedPayload{TicketID: "t1", Reason: "boom"})

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

	events, _ := h.led.Read()
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
	events, _ := h.led.Read()
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
	events, _ := h.led.Read()
	m := metrics.Compute(events)
	if m.RegressionEscapes < 1 || m.RegressionEscapeRate == 0 {
		t.Errorf("expected a regression escape; got escapes=%d rate=%v", m.RegressionEscapes, m.RegressionEscapeRate)
	}
}
