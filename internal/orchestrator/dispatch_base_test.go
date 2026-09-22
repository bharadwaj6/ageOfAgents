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
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
	"github.com/stretchr/testify/require"
)

// worktreeBaseBackend records the commit each Task's worktree was cut from,
// read before the agent writes anything, then delegates.
type worktreeBaseBackend struct {
	inner agent.Backend
	mu    sync.Mutex
	bases map[string]string // ticket ID -> base commit
}

func (b *worktreeBaseBackend) Name() string { return "worktree-base" }

func (b *worktreeBaseBackend) Run(ctx context.Context, task agent.Task) (agent.Result, error) {
	out, err := exec.CommandContext(ctx, "git", "-C", task.Worktree, "rev-parse", "HEAD").Output()
	if err != nil {
		return agent.Result{}, err
	}
	b.mu.Lock()
	b.bases[task.TicketID] = strings.TrimSpace(string(out))
	b.mu.Unlock()
	return b.inner.Run(ctx, task)
}

func (b *worktreeBaseBackend) base(ticketID string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bases[ticketID]
}

// parkingGate returns a Gate that reports it is running, waits for the test to
// release it, then reports the verdict pass asks for. While it waits, the merge
// queue is holding its candidate merge on the repository's HEAD. The wait is
// bounded so a regression fails the test rather than hanging it.
func parkingGate(t *testing.T, pass bool) (gate verify.Verifier, running, release string) {
	t.Helper()
	dir := t.TempDir()
	running, release = filepath.Join(dir, "running"), filepath.Join(dir, "release")
	verdict := "exit 1"
	if pass {
		verdict = "exit 0"
	}
	script := "touch " + running +
		"; i=0; while [ ! -e " + release + " ] && [ $i -lt 3000 ]; do sleep 0.01; i=$((i+1)); done; " + verdict
	return verify.Verifier{Commands: []verify.Command{{"sh", "-c", script}}}, running, release
}

// waitFor polls cond until it holds, failing the test if it never does.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// readyTicket appends the events that put a fresh ticket in the ready state,
// as steps 1 and 2 of a pass would.
func (h *harness) readyTicket(t *testing.T, goalID, ticketID, title string) {
	t.Helper()
	h.appendAt(t, time.Now(), api.TicketCreated, api.TicketCreatedPayload{
		TicketID: ticketID, GoalID: goalID, Title: title, IdempotencyKey: goalID + ":impl",
	})
	h.appendAt(t, time.Now(), api.TicketReady, api.TicketReadyPayload{TicketID: ticketID})
}

// In local delivery mode the merge queue merges a proposal onto the adopted
// repository's HEAD and only then runs the Gate, so for as long as the Gate runs
// — minutes, for a real test suite — HEAD carries a candidate merge nothing has
// verified. A task dispatched in that window used to cut its worktree from that
// HEAD and inherit a change the Gate was about to reject and roll back (#157).
// PR mode was never exposed: there worktrees are cut from the Goal branch, which
// only a Gate pass advances (ADR 016).
func TestDispatchDuringGateCutsFromVerifiedCommit(t *testing.T) {
	requireGit(t)
	gate, running, release := parkingGate(t, false)
	rec := &worktreeBaseBackend{inner: agent.NewMock(), bases: map[string]string{}}
	o, h := setup(t, rec, gate, Options{Concurrency: 1, MaxAttempts: 1})
	ctx := context.Background()
	h.submitGoal(t, "g1", "first change")

	// One pass dispatches g1-impl; its proposal lands asynchronously (ADR 013).
	require.NoError(t, o.ReconcileOnce(ctx))
	waitFor(t, "g1-impl to be proposed", func() bool {
		tk := h.state(t).Tickets["g1-impl"]
		return tk != nil && tk.Status == state.StatusProposed
	})
	pre, err := h.repo.Head(ctx)
	require.NoError(t, err)

	// The next pass merges that proposal and parks in the Gate.
	done := make(chan error, 1)
	go func() { done <- o.ReconcileOnce(ctx) }()
	waitFor(t, "the Gate to start", func() bool { _, err := os.Stat(running); return err == nil })
	candidate, err := h.repo.Head(ctx)
	require.NoError(t, err)
	require.NotEqual(t, pre, candidate, "the unverified candidate merge should be on HEAD while the Gate runs")

	// A second task is dispatched inside that window, exactly as a dispatch
	// goroutine launched by an earlier pass would be.
	h.submitGoal(t, "g2", "second change")
	h.readyTicket(t, "g2", "g2-impl", "Implement: second change")
	o.dispatch(ctx, dispatchJob{ticketID: "g2-impl", goalID: "g2", title: "Implement: second change", goalText: "second change", attempt: 1})

	require.NoError(t, os.WriteFile(release, nil, 0o644))
	require.NoError(t, <-done)

	require.Equal(t, pre, rec.base("g2-impl"),
		"a task dispatched while the Gate ran must be cut from the last verified commit, not from the candidate merge")
	head, err := h.repo.Head(ctx)
	require.NoError(t, err)
	require.Equal(t, pre, head, "the rejected merge should have been rolled back")
}

// The base a task is cut from must still follow the Gate: work that passed and
// merged has to be visible to whatever is dispatched next, or every task after
// the first would start from the state of the run's first pass.
func TestDispatchAfterMergeCutsFromTheMergedCommit(t *testing.T) {
	requireGit(t)
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	rec := &worktreeBaseBackend{inner: agent.NewMock(), bases: map[string]string{}}
	o, h := setup(t, rec, pass, Options{Concurrency: 1, MaxAttempts: 1})
	ctx := context.Background()
	h.submitGoal(t, "g1", "first change")

	require.NoError(t, o.ReconcileOnce(ctx))
	waitFor(t, "g1-impl to be proposed", func() bool {
		tk := h.state(t).Tickets["g1-impl"]
		return tk != nil && tk.Status == state.StatusProposed
	})
	require.NoError(t, o.ReconcileOnce(ctx)) // merges g1-impl
	require.Equal(t, state.StatusMerged, h.state(t).Tickets["g1-impl"].Status)
	merged, err := h.repo.Head(ctx)
	require.NoError(t, err)

	h.submitGoal(t, "g2", "second change")
	h.readyTicket(t, "g2", "g2-impl", "Implement: second change")
	require.NoError(t, o.ReconcileOnce(ctx)) // dispatches g2-impl
	waitFor(t, "g2-impl to be proposed", func() bool {
		tk := h.state(t).Tickets["g2-impl"]
		return tk != nil && tk.Status == state.StatusProposed
	})

	require.Equal(t, merged, rec.base("g2-impl"),
		"a task dispatched after a merge passed the Gate must be cut from it")
}
