package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/filelock"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// newMockWorkspace scaffolds a workspace on the offline mock backend.
func newMockWorkspace(t *testing.T) (string, workspace) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	tmp := t.TempDir()
	if err := cmdInit([]string{"--path", tmp, "--repo", "./demo"}); err != nil {
		t.Fatalf("init: %v", err)
	}
	ws, err := workspaceAt(tmp)
	if err != nil {
		t.Fatalf("workspaceAt: %v", err)
	}
	return tmp, ws
}

// holdSchedulerLock takes the workspace's Scheduler lock through a handle of
// its own, as a second `aoa run` would, and returns a func that releases it.
// The lock is released when the test ends if it has not been already.
func holdSchedulerLock(t *testing.T, ws workspace) (release func()) {
	t.Helper()
	f, err := os.OpenFile(schedulerLockPath(ws), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatalf("open scheduler lock: %v", err)
	}
	if err := filelock.Lock(f); err != nil {
		t.Fatalf("lock scheduler: %v", err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			if err := filelock.Unlock(f); err != nil {
				t.Errorf("unlock scheduler: %v", err)
			}
			if err := f.Close(); err != nil {
				t.Errorf("close scheduler lock: %v", err)
			}
		})
	}
	t.Cleanup(release)
	return release
}

// foldWorkspace replays the workspace's Event Log.
func foldWorkspace(t *testing.T, ws workspace) ([]api.Event, *state.State) {
	t.Helper()
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	events, err := led.Read()
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	s, err := state.Fold(events)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	return events, s
}

// countEvents counts the events of type typ on the workspace's log.
func countEvents(t *testing.T, ws workspace, typ api.EventType) int {
	t.Helper()
	events, _ := foldWorkspace(t, ws)
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// ADR 003: exactly one Scheduler reconciles a workspace. A second `aoa run`
// used to reconcile anyway, racing the first over the worktrees and the Merge
// Queue. It must refuse, and say so with an exit status a front door can key on.
func TestRunRefusesWhileAnotherSchedulerHoldsTheWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{"until settled", nil},
		{"once", []string{"--once"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmp, ws := newMockWorkspace(t)
			holdSchedulerLock(t, ws)
			if err := cmdGoal([]string{"--path", tmp, "add", "a", "greeting"}); err != nil {
				t.Fatalf("goal: %v", err)
			}

			err := cmdRun(append([]string{"--path", tmp}, tc.flags...))
			var ee *exitError
			if !errors.As(err, &ee) {
				t.Fatalf("cmdRun = %v, want an *exitError", err)
			}
			if ee.code != exitSchedulerBusy {
				t.Errorf("exit code = %d, want %d", ee.code, exitSchedulerBusy)
			}
			if !strings.Contains(err.Error(), "another aoa run") {
				t.Errorf("error should say another run holds the workspace, got %q", err)
			}
			if n := countEvents(t, ws, api.TicketCreated); n != 0 {
				t.Errorf("a refused run reconciled anyway: %d TicketCreated on the log", n)
			}
		})
	}
}

// adoptSameRepo scaffolds a second workspace that adopts the repository ws
// already reconciles, as `aoa init --adopt` lets anyone do.
func adoptSameRepo(t *testing.T, root string) (string, workspace) {
	t.Helper()
	tmp := t.TempDir()
	if err := cmdInit([]string{"--path", tmp, "--adopt", filepath.Join(root, "demo")}); err != nil {
		t.Fatalf("init adopting the same repo: %v", err)
	}
	ws, err := workspaceAt(tmp)
	if err != nil {
		t.Fatalf("workspaceAt: %v", err)
	}
	return tmp, ws
}

// The Scheduler lock lived in the workspace, and two workspaces may adopt one
// repository — so both were granted a Scheduler over the same working tree,
// each merge queue blind to the other's merges and rollbacks (#131). The lock
// has to cover the repository too, or ADR 002's linearizable branch does not
// survive the second workspace.
func TestSchedulerLockCoversTheRepositoryNotJustTheWorkspace(t *testing.T) {
	root, ws := newMockWorkspace(t)
	_, other := adoptSameRepo(t, root)

	release, err := acquireSchedulerLock(ws)
	if err != nil {
		t.Fatalf("acquireSchedulerLock: %v", err)
	}
	if _, err := acquireSchedulerLock(other); !errors.Is(err, errSchedulerBusy) {
		t.Fatalf("a second workspace adopting the same repository got a Scheduler: err = %v", err)
	} else if strings.Contains(err.Error(), "picked up") {
		// The holder replays its own Event Log; it never sees this workspace's
		// goals, so telling a front door they are in hand would be a lie.
		t.Errorf("refusal claims the holder picks this workspace's work up: %q", err)
	}

	// Not a global lock: a workspace with a repository of its own is unaffected.
	_, elsewhere := newMockWorkspace(t)
	releaseElsewhere, err := acquireSchedulerLock(elsewhere)
	if err != nil {
		t.Fatalf("a workspace on its own repository was refused: %v", err)
	}
	if err := releaseElsewhere(); err != nil {
		t.Fatalf("release: %v", err)
	}

	// And the repository is handed over once its holder is done.
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	releaseOther, err := acquireSchedulerLock(other)
	if err != nil {
		t.Fatalf("the repository stayed locked after its holder released it: %v", err)
	}
	if err := releaseOther(); err != nil {
		t.Fatalf("release: %v", err)
	}
}

// End to end, as the collision actually arrives: two workspaces adopting one
// repository, both told to run. Before the repository lock this lost work —
// one workspace's merge queue rolled a verification failure back over the
// other's merge, leaving a `Merged` event on the log for a commit no longer on
// the branch. Whichever run is refused must be refused with the busy status,
// and its goal must still land when it runs again.
func TestConcurrentRunsOnOneRepositoryLoseNoMergedWork(t *testing.T) {
	root, ws := newMockWorkspace(t)
	other, otherWS := adoptSameRepo(t, root)
	repo := filepath.Join(root, "demo")
	if err := cmdGoal([]string{"--path", root, "add", "a", "greeting"}); err != nil {
		t.Fatalf("goal: %v", err)
	}
	if err := cmdGoal([]string{"--path", other, "add", "a", "farewell"}); err != nil {
		t.Fatalf("goal: %v", err)
	}

	paths := []string{root, other}
	errs := make([]error, len(paths))
	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = cmdRun([]string{"--path", path})
		}()
	}
	wg.Wait()

	// A run that lost the repository says so with exit 75 and reconciles
	// nothing; running it again, now that the repository is free, lands it.
	for i, err := range errs {
		if err == nil {
			continue
		}
		var ee *exitError
		if !errors.As(err, &ee) || ee.code != exitSchedulerBusy {
			t.Fatalf("run %d: %v, want nil or the busy exit status", i, err)
		}
		if err := cmdRun([]string{"--path", paths[i]}); err != nil {
			t.Fatalf("re-run %d after the repository was free: %v", i, err)
		}
	}

	onBranch := map[string]bool{}
	for _, sha := range strings.Fields(runGit(t, repo, "log", "--format=%H", "HEAD")) {
		onBranch[sha] = true
	}
	for _, w := range []workspace{ws, otherWS} {
		events, s := foldWorkspace(t, w)
		if !s.Settled() {
			t.Errorf("%s: work not settled", w.root)
		}
		merged := 0
		for _, e := range events {
			if e.Type != api.Merged {
				continue
			}
			var p api.MergedPayload
			if err := e.DecodePayload(&p); err != nil {
				t.Fatalf("decode Merged: %v", err)
			}
			merged++
			if !onBranch[p.Commit] {
				t.Errorf("%s: the log records %s merged as %s, but that commit is not on the branch",
					w.root, p.TicketID, p.Commit)
			}
		}
		if merged == 0 {
			t.Errorf("%s: nothing merged", w.root)
		}
	}
}

// A front door appends its goal and then tries the lock. A goal that lands
// while the Scheduler still holds the lock — after its last look at the log but
// before it lets go — must be reconciled by that Scheduler: the front door's own
// run was refused, so otherwise nobody would.
func TestRunReconcilesGoalAppendedBeforeRelease(t *testing.T) {
	tmp, ws := newMockWorkspace(t)
	if err := cmdGoal([]string{"--path", tmp, "add", "a", "greeting"}); err != nil {
		t.Fatalf("goal: %v", err)
	}

	old := beforeSchedulerUnlock
	t.Cleanup(func() { beforeSchedulerUnlock = old })
	appended := false
	beforeSchedulerUnlock = func() {
		if appended {
			return
		}
		appended = true
		led, err := ledger.Open(ws.ledgerPath) // the front door's own handle
		if err != nil {
			t.Errorf("front door: open ledger: %v", err)
			return
		}
		if _, err := submitGoal(led, goalRequest{Text: "add a farewell", Source: "front-door"}); err != nil {
			t.Errorf("front door: submit goal: %v", err)
		}
	}

	if err := cmdRun([]string{"--path", tmp}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !appended {
		t.Fatal("beforeSchedulerUnlock never ran")
	}

	_, s := foldWorkspace(t, ws)
	if len(s.Goals) != 2 {
		t.Fatalf("goals = %d, want 2", len(s.Goals))
	}
	merged := map[string]int{}
	for _, tk := range s.Tickets {
		if tk.Status == state.StatusMerged {
			merged[tk.GoalID]++
		}
	}
	for id, g := range s.Goals {
		if merged[id] == 0 {
			t.Errorf("goal %s (%q) was left unreconciled on the log", id, g.Text)
		}
	}
	if !s.Settled() {
		t.Error("work should be settled")
	}
}

// The re-check is what makes a refused front door safe, but --once promises a
// single pass: it must not loop on work that arrived mid-pass.
func TestWithSchedulerLockRechecksOnlyWhenAsked(t *testing.T) {
	for _, tc := range []struct {
		name      string
		recheck   bool
		wantCalls int
	}{
		{"recheck", true, 2},
		{"no recheck", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ws := newMockWorkspace(t)
			led, err := ledger.Open(ws.ledgerPath)
			if err != nil {
				t.Fatalf("open ledger: %v", err)
			}

			old := beforeSchedulerUnlock
			t.Cleanup(func() { beforeSchedulerUnlock = old })
			appends := 0
			beforeSchedulerUnlock = func() {
				if appends > 0 {
					return
				}
				appends++
				if _, err := submitGoal(led, goalRequest{Text: "late goal", Source: "front-door"}); err != nil {
					t.Errorf("submit goal: %v", err)
				}
			}

			calls := 0
			err = withSchedulerLock(ws, led, tc.recheck, func() error {
				calls++
				return nil
			})
			if err != nil {
				t.Fatalf("withSchedulerLock: %v", err)
			}
			if calls != tc.wantCalls {
				t.Errorf("fn ran %d times, want %d", calls, tc.wantCalls)
			}
		})
	}
}

// `aoa run --interval` holds the lock for a pass, not for its lifetime: a pass
// that finds another Scheduler reconciling is skipped rather than fatal, and a
// later pass picks the work up once the lock is free.
func TestRunEverySkipsBusyPass(t *testing.T) {
	tmp, ws := newMockWorkspace(t)
	if err := cmdGoal([]string{"--path", tmp, "add", "a", "greeting"}); err != nil {
		t.Fatalf("goal: %v", err)
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	o, err := buildOrchestrator(ws, led, state.Budget{})
	if err != nil {
		t.Fatalf("buildOrchestrator: %v", err)
	}
	release := holdSchedulerLock(t, ws)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runEvery(ctx, o, led, ws, 10*time.Millisecond) }()

	// Several ticks pass while another Scheduler holds the workspace.
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("runEvery gave up on a busy workspace: %v", err)
	default:
	}
	if n := countEvents(t, ws, api.TicketCreated); n != 0 {
		t.Fatalf("a busy pass reconciled anyway: %d TicketCreated on the log", n)
	}

	// Once the lock is free, the next pass reconciles the goal.
	release()
	deadline := time.Now().Add(30 * time.Second)
	for countEvents(t, ws, api.Merged) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no pass reconciled the goal after the lock was released")
		}
		time.Sleep(20 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("runEvery returned %v, want nil on cancel", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runEvery did not return after cancel")
	}
}

func TestExitCode(t *testing.T) {
	busy := &exitError{code: exitSchedulerBusy, err: errSchedulerBusy}
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"plain error", errors.New("boom"), 1},
		{"exitError", busy, exitSchedulerBusy},
		{"wrapped exitError", fmt.Errorf("run: %w", busy), exitSchedulerBusy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCode(tc.err); got != tc.want {
				t.Errorf("exitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
	if !errors.Is(busy, errSchedulerBusy) {
		t.Error("exitError must unwrap to the error it carries")
	}
	if busy.Error() != errSchedulerBusy.Error() {
		t.Errorf("exitError message = %q, want the carried error's", busy.Error())
	}
}
