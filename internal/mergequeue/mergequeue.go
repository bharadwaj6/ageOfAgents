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

// verifyFailureReason describes why the gate failed, distinguishing a real
// verdict on the proposal from a sandbox that could not run it at all. Both
// block the merge; only the first says anything about the code.
func verifyFailureReason(res verify.Result) string {
	if res.Infra {
		return "gate could not run (sandbox failure): " + res.Failed
	}
	return "verification failed: " + res.Failed
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
	// RegressionEscaped is set when the merge passed the Gate but a broader
	// Shadow verifier failed on the post-merge state — a verification blind spot
	// the Gate let through. It is observational: the merge is kept (the Gate is
	// the contract), and ShadowReason records what the broader set caught.
	RegressionEscaped bool
	ShadowReason      string
}

// Queue verifies and merges proposals one at a time.
type Queue struct {
	Repo     *worktree.Repo
	Verifier verify.Verifier
	// Shadow is an optional broader test set run against post-merge main after a
	// proposal passes the Gate. It never blocks or rolls back a merge; it only
	// measures the regression-escape rate (the Gate's blind spot). Empty = off.
	Shadow verify.Verifier
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
		// Conflict (or merge failure). Merge attempts its own `merge --abort`, but
		// that is best-effort and can itself fail (an `index.lock` under load is
		// enough) — which would leave main mid-merge while we reported a tidy
		// rejection. Restoring pre here is what actually makes "main is always
		// green" true; if even that fails, main's state is unknown and the caller
		// must hear about it as an error, not a verdict on the proposal.
		if rbErr := q.Repo.ResetHard(ctx, pre); rbErr != nil {
			return out, fmt.Errorf("rollback after failed merge (%v): %w", err, rbErr)
		}
		out.Reason = fmt.Sprintf("merge failed: %v", err)
		return out, nil
	}

	res := q.Verifier.Run(ctx, q.Repo.Dir)
	out.Output = res.Output
	if !res.Passed {
		if rbErr := q.Repo.ResetHard(ctx, pre); rbErr != nil {
			return out, fmt.Errorf("rollback after failed verify: %w", rbErr)
		}
		out.Reason = verifyFailureReason(res)
		return out, nil
	}

	out.Verified = true
	out.Merged = true
	out.MergeCommit = mergeSHA

	// Shadow check: run the broader test set against the kept merge. A failure
	// here is a regression the Gate missed — record it, but keep the merge (the
	// Gate is the merge contract; the shadow only measures its blind spot).
	if len(q.Shadow.Commands) > 0 {
		if sres := q.Shadow.Run(ctx, q.Repo.Dir); !sres.Passed {
			out.RegressionEscaped = true
			out.ShadowReason = sres.Failed
		}
	}
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
		// Conflict. As in Process, restore pre ourselves rather than trusting the
		// best-effort abort inside Merge.
		if rbErr := q.Repo.ResetHard(ctx, pre); rbErr != nil {
			return out, fmt.Errorf("rollback after failed merge (%v): %w", err, rbErr)
		}
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
		out.Reason = verifyFailureReason(res)
		return out, nil
	}

	out.Verified = true
	out.MergeCommit = mergeSHA // candidate commit, informational only
	out.Reason = "dry-run verified"
	return out, nil
}

// ProcessBatch verifies a set of proposals, batching ones that touch disjoint
// file sets into a single Gate run instead of one each — the throughput win
// when work has good locality, without speculation. Proposals with disjoint
// files cannot textually conflict, so they merge cleanly together; the Gate runs
// once on the union. If that batch Gate fails, the whole batch is rolled back and
// its members are re-processed one at a time to isolate the culprit (so a single
// bad proposal never sinks good neighbours). Overlapping or undeterminable
// proposals always serialize. `main` stays linearizable and green either way.
//
// Outcomes are returned aligned with props. ProcessBatch is *not* used when a
// Shadow set is configured (regression-escape attribution needs per-proposal
// Gate runs) — the orchestrator falls back to Process there.
func (q *Queue) ProcessBatch(ctx context.Context, props []Proposal) ([]Outcome, error) {
	outcomes := make([]Outcome, len(props))

	// Greedily select a maximal prefix of proposals with pairwise-disjoint files.
	used := map[string]bool{}
	batch := make([]int, 0, len(props))
	serial := make([]int, 0, len(props))
	for i, p := range props {
		files, err := q.Repo.ChangedFiles(ctx, p.Branch)
		if err != nil || len(files) == 0 || overlaps(files, used) {
			serial = append(serial, i)
			continue
		}
		for _, f := range files {
			used[f] = true
		}
		batch = append(batch, i)
	}

	// A batch of <2 buys nothing — serialize everything.
	if len(batch) < 2 {
		for i, p := range props {
			out, err := q.Process(ctx, p)
			if err != nil {
				return outcomes, err
			}
			outcomes[i] = out
		}
		return outcomes, nil
	}

	pre, err := q.Repo.Head(ctx)
	if err != nil {
		return outcomes, fmt.Errorf("read head: %w", err)
	}

	// Merge the disjoint batch (cheap, conflict-free) then Gate once.
	merged := true
	for _, i := range batch {
		sha, err := q.Repo.Merge(ctx, props[i].Branch, fmt.Sprintf("merge: %s", props[i].TicketID))
		if err != nil {
			merged = false // unexpected for disjoint files; fall back to serial
			break
		}
		outcomes[i] = Outcome{TicketID: props[i].TicketID, Worker: props[i].Worker, MergeCommit: sha}
	}
	if merged {
		res := q.Verifier.Run(ctx, q.Repo.Dir)
		if res.Passed {
			for _, i := range batch {
				o := outcomes[i]
				o.Verified, o.Merged, o.Output = true, true, res.Output
				outcomes[i] = o
			}
		} else {
			merged = false
		}
	}
	if !merged {
		// Batch Gate failed (or a merge hiccuped): discard the lot and isolate.
		if rbErr := q.Repo.ResetHard(ctx, pre); rbErr != nil {
			return outcomes, fmt.Errorf("rollback after batch: %w", rbErr)
		}
		for _, i := range batch {
			out, err := q.Process(ctx, props[i])
			if err != nil {
				return outcomes, err
			}
			outcomes[i] = out
		}
	}

	// Process overlapping / undeterminable proposals one at a time, on top.
	for _, i := range serial {
		out, err := q.Process(ctx, props[i])
		if err != nil {
			return outcomes, err
		}
		outcomes[i] = out
	}
	return outcomes, nil
}

// overlaps reports whether any of files is already in used.
func overlaps(files []string, used map[string]bool) bool {
	for _, f := range files {
		if used[f] {
			return true
		}
	}
	return false
}
