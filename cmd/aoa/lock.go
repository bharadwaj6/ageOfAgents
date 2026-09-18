package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/bharadwaj6/ageOfAgents/internal/filelock"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
)

// exitSchedulerBusy is the exit status of an `aoa run` that found another
// Scheduler reconciling its workspace: EX_TEMPFAIL from sysexits.h, "try again
// later". A front door can tell it apart from a failed run (1), and it needs no
// retry — the goal it submitted is already on the log.
const exitSchedulerBusy = 75

// errSchedulerBusy reports that another process, or another handle in this one,
// holds the workspace's Scheduler lock.
var errSchedulerBusy = errors.New("another aoa run is reconciling this workspace; work already on the log will be picked up")

// beforeSchedulerUnlock runs just before withSchedulerLock releases the lock. It
// does nothing outside tests, which use it to append a goal inside the window
// the re-check exists to close.
var beforeSchedulerUnlock = func() {}

// schedulerLockPath is the file a Scheduler locks to own its workspace:
// <ws>/.aoa/scheduler.lock. It is never deleted, and never needs to be: the OS
// drops the lock when its holder exits, however it exits, so a crashed run
// cannot leave a stale lock behind.
func schedulerLockPath(ws workspace) string {
	return filepath.Join(filepath.Dir(ws.ledgerPath), "scheduler.lock")
}

// acquireSchedulerLock takes the workspace's Scheduler lock without waiting,
// returning errSchedulerBusy when it is held elsewhere. The caller must call
// release when it stops reconciling.
func acquireSchedulerLock(ws workspace) (release func() error, err error) {
	path := schedulerLockPath(ws)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create scheduler lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open scheduler lock: %w", err)
	}
	ok, err := filelock.TryLock(f)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("lock scheduler: %w", err), f.Close())
	}
	if !ok {
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("close scheduler lock: %w", err)
		}
		return nil, errSchedulerBusy
	}
	return func() error {
		var unlockErr, closeErr error
		if err := filelock.Unlock(f); err != nil {
			unlockErr = fmt.Errorf("unlock scheduler: %w", err)
		}
		if err := f.Close(); err != nil {
			closeErr = fmt.Errorf("close scheduler lock: %w", err)
		}
		return errors.Join(unlockErr, closeErr)
	}, nil
}

// withSchedulerLock runs fn as the workspace's one Scheduler (ADR 003). When
// another process holds the lock it returns errSchedulerBusy without calling fn.
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
