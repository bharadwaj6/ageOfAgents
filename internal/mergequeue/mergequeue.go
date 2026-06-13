// Package mergequeue serializes writes to the integration branch behind the
// objective verifier (docs/v2/adr/002-verifier-gated-merge-queue.md).
//
// For each proposal it merges the branch into main, runs the verifier against
// the *post-merge* state, and keeps the merge only if it passes; otherwise it
// rolls main back. This keeps `main` linearizable and always green. The queue
// is the only writer to main; it does not block worker dispatch.
package mergequeue

import (
	"context"
	"fmt"

	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
)

// Proposal is a candidate change awaiting verification + merge.
type Proposal struct {
	TicketID string
	Worker   string
	Branch   string
}

// Outcome reports what happened to a proposal. A rejected proposal (merge
// conflict or failed verification) is a normal Outcome, not a Go error; errors
// are reserved for unexpected infrastructure failures.
type Outcome struct {
	TicketID    string
	Worker      string
	Merged      bool
	MergeCommit string // set when Merged
	Reason      string // set when !Merged
	Output      string // verifier output (when verification ran)
}

// Queue verifies and merges proposals one at a time.
type Queue struct {
	Repo     *worktree.Repo
	Verifier verify.Verifier
}

// New constructs a Queue.
func New(repo *worktree.Repo, v verify.Verifier) *Queue {
	return &Queue{Repo: repo, Verifier: v}
}

// Process handles a single proposal: merge → verify → keep or roll back.
func (q *Queue) Process(ctx context.Context, p Proposal) (Outcome, error) {
	out := Outcome{TicketID: p.TicketID, Worker: p.Worker}

	pre, err := q.Repo.Head(ctx)
	if err != nil {
		return out, fmt.Errorf("read head: %w", err)
	}

	mergeSHA, err := q.Repo.Merge(ctx, p.Branch, fmt.Sprintf("merge: %s", p.TicketID))
	if err != nil {
		// Conflict (or merge failure): main was aborted back to pre. Reject.
		out.Reason = fmt.Sprintf("merge failed: %v", err)
		return out, nil
	}

	res := q.Verifier.Run(ctx, q.Repo.Dir)
	out.Output = res.Output
	if !res.Passed {
		if rbErr := q.Repo.ResetHard(ctx, pre); rbErr != nil {
			return out, fmt.Errorf("rollback after failed verify: %w", rbErr)
		}
		out.Reason = "verification failed: " + res.Failed
		return out, nil
	}

	out.Merged = true
	out.MergeCommit = mergeSHA
	return out, nil
}
