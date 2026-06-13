// Package mergequeue serializes writes to the integration branch behind the
// objective verifier (docs/design/adr/002-verifier-gated-merge-queue.md).
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
	Verified    bool   // the candidate passed the Gate (always set when verification ran)
	MergeCommit string // set when Merged, or the candidate commit for a passing DryRun
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

	out.Verified = true
	out.Merged = true
	out.MergeCommit = mergeSHA
	return out, nil
}

// DryRun merges the proposal, runs the Gate against the post-merge state, then
// *always* rolls main back — reporting whether the proposal would merge cleanly
// without ever writing to main. The human-in-the-loop approval gate (ADR 008)
// uses it to present a Gate-verified candidate before a person decides, so a
// pending approval never leaves main in a half-merged state.
func (q *Queue) DryRun(ctx context.Context, p Proposal) (Outcome, error) {
	out := Outcome{TicketID: p.TicketID, Worker: p.Worker}

	pre, err := q.Repo.Head(ctx)
	if err != nil {
		return out, fmt.Errorf("read head: %w", err)
	}

	mergeSHA, err := q.Repo.Merge(ctx, p.Branch, fmt.Sprintf("merge: %s", p.TicketID))
	if err != nil {
		// Conflict: main was already aborted back to pre. Reject the candidate.
		out.Reason = fmt.Sprintf("merge failed: %v", err)
		return out, nil
	}

	res := q.Verifier.Run(ctx, q.Repo.Dir)
	out.Output = res.Output
	// A dry run never keeps the merge, pass or fail.
	if rbErr := q.Repo.ResetHard(ctx, pre); rbErr != nil {
		return out, fmt.Errorf("rollback after dry run: %w", rbErr)
	}
	if !res.Passed {
		out.Reason = "verification failed: " + res.Failed
		return out, nil
	}

	out.Verified = true
	out.MergeCommit = mergeSHA // candidate commit, informational only
	out.Reason = "dry-run verified"
	return out, nil
}
