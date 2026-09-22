package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bharadwaj6/ageOfAgents/internal/config"
	"github.com/bharadwaj6/ageOfAgents/internal/filelock"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
)

// exitSchedulerBusy is the exit status of an `aoa run` that found another
// Scheduler already reconciling: EX_TEMPFAIL from sysexits.h, "try again
// later". A front door can tell it apart from a failed run (1).
const exitSchedulerBusy = 75

// errSchedulerBusy marks a run refused because another Scheduler holds what it
// would reconcile. The two refusals below wrap it, so callers test for "busy"
// with errors.Is and never on the message.
var errSchedulerBusy = errors.New("another aoa run is reconciling")

// errWorkspaceBusy reports that another process, or another handle in this one,
// holds this workspace's Scheduler lock. That Scheduler reads this same Event
// Log, so a goal already appended needs no retry — it will be picked up.
var errWorkspaceBusy = fmt.Errorf("%w this workspace; work already on the log will be picked up", errSchedulerBusy)

// repoBusy reports that a Scheduler in a *different* workspace holds the
// repository this one adopted. That Scheduler replays its own Event Log and
// never sees this workspace's goals, so — unlike errWorkspaceBusy — nothing
// here is picked up for us: this run must be retried once the repository is
// free.
func repoBusy(repoDir string) error {
	return fmt.Errorf("%w the repository at %s, from another workspace; this workspace's goals wait for its next run", errSchedulerBusy, repoDir)
}

// beforeSchedulerUnlock runs just before withSchedulerLock releases the lock. It
// does nothing outside tests, which use it to append a goal inside the window
// the re-check exists to close.
var beforeSchedulerUnlock = func() {}

// schedulerLockPath is the file a Scheduler locks to own its workspace:
// <ws>/.aoa/scheduler.lock.
func schedulerLockPath(ws workspace) string {
	return filepath.Join(filepath.Dir(ws.ledgerPath), "scheduler.lock")
}

// repoLockPath is the file a Scheduler locks to own the repository it
// reconciles: <git-dir>/aoa.lock, which for an ordinary checkout is
// <repo>/.git/aoa.lock. It also reports the repository's configured path, for
// the message a refused run prints.
//
// The workspace lock guards one Event Log. This one guards the git working
// tree, which two workspaces can adopt at the same time (#131) — and which they
// then write concurrently, each merge queue blind to the other's merges and
// rollbacks. Git resolves the path, so two workspaces that spelled the same
// repository differently still meet on the same lock.
func repoLockPath(ws workspace) (path, repoDir string, err error) {
	cfg, err := config.Load(ws.configPath)
	if err != nil {
		return "", "", err
	}
	repoDir = resolve(ws.root, cfg.Repo)
	gitDir, err := worktree.OpenRepo(repoDir).GitDir(context.Background())
	if err != nil {
		return "", "", fmt.Errorf("locate the git directory of %s: %w", repoDir, err)
	}
	return filepath.Join(gitDir, "aoa.lock"), repoDir, nil
}

// acquireSchedulerLock takes the locks a Scheduler needs to reconcile without
// waiting for either: its workspace, then the repository that workspace
// adopted. It returns an error wrapping errSchedulerBusy when one is held
// elsewhere. The caller must call release when it stops reconciling.
func acquireSchedulerLock(ws workspace) (release func() error, err error) {
	releaseWorkspace, err := lockFile(schedulerLockPath(ws), errWorkspaceBusy)
	if err != nil {
		return nil, err
	}
	undo := func(err error) (func() error, error) {
		return nil, errors.Join(err, releaseWorkspace())
	}
	path, repoDir, err := repoLockPath(ws)
	if err != nil {
		return undo(err)
	}
	releaseRepo, err := lockFile(path, repoBusy(repoDir))
	if err != nil {
		return undo(err)
	}
	return func() error { return errors.Join(releaseRepo(), releaseWorkspace()) }, nil
}

// lockFile takes an exclusive advisory lock on path without waiting, creating
// the file if it is not there, and returns busy when another handle holds it.
// The file is never deleted, and never needs to be: the OS drops the lock when
// its holder exits, however it exits, so a crashed run cannot leave a stale
// lock behind.
func lockFile(path string, busy error) (release func() error, err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create dir for %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	ok, err := filelock.TryLock(f)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("lock %s: %w", path, err), f.Close())
	}
	if !ok {
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("close %s: %w", path, err)
		}
		return nil, busy
	}
	return func() error {
		var unlockErr, closeErr error
		if err := filelock.Unlock(f); err != nil {
			unlockErr = fmt.Errorf("unlock %s: %w", path, err)
		}
		if err := f.Close(); err != nil {
			closeErr = fmt.Errorf("close %s: %w", path, err)
		}
		return errors.Join(unlockErr, closeErr)
	}, nil
}

// withSchedulerLock runs fn as the one Scheduler of this workspace and of the
// repository it adopted (ADR 003). When another process holds either lock it
// returns an error wrapping errSchedulerBusy without calling fn.
//
// With recheck set, it also guarantees that no goal is stranded by the handoff.
// A submitter appends its goal to the log before it tries the lock, so after
// releasing, this looks at the log again: anything appended while the lock was
// held is reconciled by running fn once more, under a fresh lock. Any goal
// appended after the release belongs to a submitter whose own attempt at the
// lock — or the next scheduled run — succeeds. And if another Scheduler takes
// the lock first, the new work is its to reconcile, so that is not an error.
func withSchedulerLock(ws workspace, led *ledger.Ledger, recheck bool, fn func() error) error {
	for first := true; ; first = false {
		release, err := acquireSchedulerLock(ws)
		if errors.Is(err, errSchedulerBusy) && !first {
			return nil // the new holder re-checks the log itself
		}
		if err != nil {
			return err
		}
		err = fn()
		seqAtEnd := 0
		if err == nil && recheck {
			seqAtEnd, err = lastSeq(led)
		}
		beforeSchedulerUnlock()
		if rerr := release(); rerr != nil {
			err = errors.Join(err, rerr)
		}
		if err != nil || !recheck {
			return err
		}
		seq, err := lastSeq(led)
		if err != nil {
			return err
		}
		if seq <= seqAtEnd {
			return nil
		}
	}
}

// lastSeq returns the sequence number of the newest event on the log, or 0 when
// the log is empty.
func lastSeq(led *ledger.Ledger) (int, error) {
	events, err := led.Read()
	if err != nil {
		return 0, fmt.Errorf("read event log: %w", err)
	}
	if len(events) == 0 {
		return 0, nil
	}
	return events[len(events)-1].Seq, nil
}
