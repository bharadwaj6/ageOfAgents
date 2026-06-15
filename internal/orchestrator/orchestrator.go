// Package orchestrator is the Scheduler — the single deterministic control loop
// that drives the whole system (docs/design/adr/003-flat-orchestrator-worker.md):
//
//	read(Event Log) -> replay -> diff desired vs actual -> act -> append events
//
// One controller, not eleven. It decomposes Goals into Tasks, promotes
// dependency-ready Tasks, dispatches Workers under a Concurrency Limit, drives
// the Gate-verified Merge Queue, and restarts stalled Workers. All coordination
// is plain Go (no LLM); only the work itself is done by the agent Backend.
package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/mergequeue"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Options configures an Orchestrator. Zero values fall back to sane defaults.
type Options struct {
	Concurrency        int           // max Workers in flight (Concurrency Limit); default 4
	MaxAttempts        int           // attempts per Task before failing; default 2
	Conventions        string        // injected into every agent prompt (Conventions)
	WorktreeBase       string        // where per-ticket worktrees live; default <repo>/.git/aoa-worktrees
	StallTimeout       time.Duration // no-progress timeout for the Stall Detector; default 2m
	MaxPasses          int           // safety bound on Scheduler passes in Run; default 1000
	MaxGraphDepth      int           // max emergent decomposition depth (graph governor); default 5
	MaxTicketsPerGoal  int           // max tickets a single Goal may spawn (graph governor); default 64
	MaxFanOut          int           // max NEW children one decomposition may emit (graph governor); default 8
	MaxTokensPerGoal   int           // per-Goal token budget; the spend governor stops a Goal past it; 0 = unlimited
	RequireApproval    bool          // park each verified proposal for human approval before merge (ADR 008)
	RetryBackoff       time.Duration // base wait before re-dispatching a failed ticket (exponential per attempt); 0 = off
	CrashLoopThreshold int           // N identical-reason verify failures in a row → give up even under MaxAttempts; default 3
	Now                func() time.Time
	Sleep              func(time.Duration) // injectable for tests; default time.Sleep
}

// Orchestrator owns one run of the control loop.
type Orchestrator struct {
	led     *ledger.Ledger
	repo    *worktree.Repo
	backend agent.Backend
	mq      *mergequeue.Queue
	opt     Options

	mu        sync.Mutex
	worktrees map[string]*worktree.Worktree
}

// New builds an Orchestrator and fills in default options.
func New(led *ledger.Ledger, repo *worktree.Repo, backend agent.Backend, mq *mergequeue.Queue, opt Options) *Orchestrator {
	if opt.Concurrency <= 0 {
		opt.Concurrency = 4
	}
	if opt.MaxAttempts <= 0 {
		opt.MaxAttempts = 2
	}
	if opt.StallTimeout <= 0 {
		opt.StallTimeout = 2 * time.Minute
	}
	if opt.MaxPasses <= 0 {
		opt.MaxPasses = 1000
	}
	if opt.MaxGraphDepth <= 0 {
		opt.MaxGraphDepth = 5
	}
	if opt.MaxTicketsPerGoal <= 0 {
		opt.MaxTicketsPerGoal = 64
	}
	if opt.MaxFanOut <= 0 {
		opt.MaxFanOut = 8
	}
	if opt.CrashLoopThreshold <= 0 {
		opt.CrashLoopThreshold = 3
	}
	if opt.WorktreeBase == "" {
		opt.WorktreeBase = filepath.Join(repo.Dir, ".git", "aoa-worktrees")
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Sleep == nil {
		opt.Sleep = time.Sleep
	}
	return &Orchestrator{
		led: led, repo: repo, backend: backend, mq: mq, opt: opt,
		worktrees: map[string]*worktree.Worktree{},
	}
}

// Run reconciles repeatedly until the work is settled or no pass makes progress.
func (o *Orchestrator) Run(ctx context.Context) error {
	for pass := 0; pass < o.opt.MaxPasses; pass++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		before, err := o.lastSeq()
		if err != nil {
			return err
		}
		if err := o.ReconcileOnce(ctx); err != nil {
			return err
		}
		s, err := o.loadState()
		if err != nil {
			return err
		}
		if s.Settled() && o.allGoalsDecomposed(s) {
			return nil
		}
		if s.LastSeq == before {
			// No progress. If the only unsettled work is parked for human
			// approval, pause cleanly — a later `aoa approve` + `aoa run` resumes.
			if pausedForApproval(s) {
				return nil
			}
			// If the only thing blocking progress is a retry backoff, wait for the
			// nearest ticket to become dispatchable rather than failing the run.
			if wait, ok := o.nextBackoffWait(s, o.opt.Now()); ok {
				o.opt.Sleep(wait)
				continue
			}
			return fmt.Errorf("orchestrator made no progress but work is unsettled (seq %d)", s.LastSeq)
		}
	}
	return fmt.Errorf("orchestrator exceeded %d passes", o.opt.MaxPasses)
}

// ReconcileOnce performs one full reconcile pass.
func (o *Orchestrator) ReconcileOnce(ctx context.Context) error {
	// 1. Decompose goals that have no tickets yet.
	s, err := o.loadState()
	if err != nil {
		return err
	}
	for _, g := range sortedGoals(s) {
		if !o.goalHasTickets(s, g.ID) {
			if err := o.emit(api.TicketCreated, api.TicketCreatedPayload{
				TicketID:       g.ID + "-impl",
				GoalID:         g.ID,
				Title:          "Implement: " + g.Text,
				IdempotencyKey: g.ID + ":impl",
			}); err != nil {
				return err
			}
		}
	}

	// 1b. Spend governor: stop Goals that have exhausted their token budget
	// before dispatching any more work (circuit breaker).
	if o.opt.MaxTokensPerGoal > 0 {
		if s, err = o.loadState(); err != nil {
			return err
		}
		if err := o.enforceBudgets(s); err != nil {
			return err
		}
	}

	// 2. Promote dependency-ready tickets.
	if s, err = o.loadState(); err != nil {
		return err
	}
	for _, t := range s.NewlyReady() {
		if err := o.emit(api.TicketReady, api.TicketReadyPayload{TicketID: t.ID}); err != nil {
			return err
		}
	}

	// 3. Dispatch a wave of ready tickets under the concurrency governor.
	if s, err = o.loadState(); err != nil {
		return err
	}
	slots := o.opt.Concurrency - s.ActiveCount()
	ready := o.dispatchable(s.ReadyTickets(), o.opt.Now())
	n := min(slots, len(ready))
	if n > 0 {
		var wg sync.WaitGroup
		for _, t := range ready[:n] {
			job := dispatchJob{
				ticketID: t.ID,
				goalID:   t.GoalID,
				title:    t.Title,
				goalText: goalText(s, t),
				depth:    t.Depth,
				attempt:  t.Attempts + 1, // this dispatch is the next attempt
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				o.dispatch(ctx, job)
			}()
		}
		wg.Wait()
	}

	// 4. Drain the merge queue for proposed tickets (serialized writes to main).
	// When several proposals are waiting and neither the approval gate nor a
	// Shadow set is in play, batch disjoint-file ones into a single Gate run;
	// otherwise process them one at a time (the approval/shadow paths need a
	// per-proposal Gate).
	if s, err = o.loadState(); err != nil {
		return err
	}
	proposed := s.Proposed()
	if len(proposed) > 1 && !o.opt.RequireApproval && len(o.mq.Shadow.Commands) == 0 {
		props := make([]mergequeue.Proposal, len(proposed))
		for i, t := range proposed {
			props[i] = mergequeue.Proposal{TicketID: t.ID, Worker: t.Worker, Branch: t.Branch}
		}
		outcomes, err := o.mq.ProcessBatch(ctx, props)
		if err != nil {
			return err
		}
		for i, t := range proposed {
			if err := o.applyMergeOutcome(ctx, t, outcomes[i]); err != nil {
				return err
			}
		}
	} else {
		for _, t := range proposed {
			if err := o.processProposal(ctx, t); err != nil {
				return err
			}
		}
	}

	// 5. Liveness: fail tickets that can never become ready because a
	// dependency has terminally failed (no ticket waits forever on dead work).
	if s, err = o.loadState(); err != nil {
		return err
	}
	for _, t := range s.Blocked() {
		dead := s.DeadDependency(t)
		if err := o.emit(api.TicketFailed, api.TicketFailedPayload{
			TicketID: t.ID, Worker: t.Worker, Reason: "dependency " + dead + " failed",
		}); err != nil {
			return err
		}
	}

	// 6. Failure detector: restart workers with no recent progress.
	if s, err = o.loadState(); err != nil {
		return err
	}
	now := o.opt.Now()
	for _, t := range detectStalled(s, now, o.opt.StallTimeout) {
		if err := o.emit(api.WorkerStalled, api.WorkerStalledPayload{TicketID: t.ID, Worker: t.Worker}); err != nil {
			return err
		}
		if err := o.emit(api.WorkerRestarted, api.WorkerRestartedPayload{TicketID: t.ID, Worker: t.Worker}); err != nil {
			return err
		}
		o.cleanupWorktree(ctx, t.ID)
	}
	return nil
}

type dispatchJob struct {
	ticketID string
	goalID   string
	title    string
	goalText string
	depth    int
	attempt  int
}

// dispatch runs one ticket attempt: claim -> worktree -> agent -> commit ->
// propose. Failures become a retry (WorkerRestarted) or, at the attempt cap, a
// terminal TicketFailed.
func (o *Orchestrator) dispatch(ctx context.Context, j dispatchJob) {
	worker := "worker/" + j.ticketID
	if err := o.emit(api.TicketClaimed, api.TicketClaimedPayload{TicketID: j.ticketID, Worker: worker}); err != nil {
		return
	}

	branch := "aoa/" + worktree.SanitizeBranch(j.ticketID) + "-" + ShortID()
	dest := filepath.Join(o.opt.WorktreeBase, worktree.SanitizeBranch(branch))
	wt, err := o.repo.AddWorktree(ctx, dest, branch)
	if err != nil {
		o.failAttempt(ctx, j, worker, fmt.Sprintf("worktree: %v", err))
		return
	}
	o.mu.Lock()
	o.worktrees[j.ticketID] = wt
	o.mu.Unlock()

	if err := o.emit(api.WorkStarted, api.WorkStartedPayload{TicketID: j.ticketID, Worker: worker, Worktree: dest}); err != nil {
		return
	}

	res, err := o.backend.Run(ctx, agent.Task{
		TicketID:    j.ticketID,
		Title:       j.title,
		Goal:        j.goalText,
		Worktree:    dest,
		Conventions: o.opt.Conventions,
	})
	if err != nil {
		o.failAttempt(ctx, j, worker, fmt.Sprintf("agent: %v", err))
		return
	}

	// Emergent decomposition: the worker split this ticket into children rather
	// than editing code. Extend the graph via the Shared Log; nothing to commit.
	if len(res.Subtasks) > 0 {
		o.cleanupWorktree(ctx, j.ticketID)
		o.decompose(j, worker, res.Subtasks, res.Tokens, res.Model)
		return
	}

	sha, changed, err := wt.Commit(ctx, fmt.Sprintf("feat: %s (%s)", j.title, j.ticketID))
	if err != nil {
		o.failAttempt(ctx, j, worker, fmt.Sprintf("commit: %v", err))
		return
	}
	if !changed {
		o.failAttempt(ctx, j, worker, "agent produced no changes")
		return
	}

	_ = o.emit(api.ProposalSubmitted, api.ProposalSubmittedPayload{
		TicketID: j.ticketID, Worker: worker, Branch: branch, Commit: sha, Trace: res.Trace, Tokens: res.Tokens, Model: res.Model,
	})
}

// decompose turns a worker's proposed subtasks into child tickets on the Shared
// Log (emergent decomposition, ADR 006). It enforces the graph governors (depth
// and per-Goal ticket budgets) and rejects dependencies that are unknown or
// would create a cycle. A rejected decomposition fails the parent terminally —
// re-running the same worker would propose the same invalid graph. On success
// the parent becomes terminal (StatusDecomposed) and the children carry the work.
func (o *Orchestrator) decompose(j dispatchJob, worker string, subs []agent.Subtask, tokens int, model string) {
	s, err := o.loadState()
	if err != nil {
		return
	}

	// Resolve batch-local handles to stable, globally-unique child ticket IDs.
	// Subtasks sharing an idempotency key collapse to one canonical child (the
	// same dedup state.Apply performs), so the emitted Children list never names
	// a ticket that was never created — otherwise the parent would wait forever
	// on a phantom child.
	localToID := make(map[string]string, len(subs))
	keyToID := make(map[string]string, len(subs))
	adopted := make(map[string]bool) // canonical IDs that already exist in state
	for _, st := range subs {
		if st.LocalID == "" {
			o.failDecompose(j, worker, "subtask missing local id")
			return
		}
		if _, dup := localToID[st.LocalID]; dup {
			o.failDecompose(j, worker, "duplicate subtask local id "+st.LocalID)
			return
		}
		if st.IdempotencyKey != "" {
			if id, seen := keyToID[st.IdempotencyKey]; seen {
				localToID[st.LocalID] = id // duplicate logical child within this batch
				continue
			}
			if id, exists := s.TicketForKey(st.IdempotencyKey); exists {
				// The key already names a ticket (e.g. a re-decomposition after a
				// crash, or shared work): adopt it instead of creating a phantom.
				localToID[st.LocalID] = id
				keyToID[st.IdempotencyKey] = id
				adopted[id] = true
				continue
			}
		}
		id := childID(j.ticketID, st.LocalID)
		localToID[st.LocalID] = id
		if st.IdempotencyKey != "" {
			keyToID[st.IdempotencyKey] = id
		}
	}

	type child struct {
		id, title, key string
		deps           []string
		adopt          bool // already exists in state; reference it, do not re-create
	}
	children := make([]child, 0, len(subs))
	childIDs := make([]string, 0, len(subs))
	newEdges := make(map[string][]string, len(subs))
	childSet := make(map[string]bool, len(subs))
	for _, id := range localToID {
		childSet[id] = true
	}
	for _, st := range subs {
		id := localToID[st.LocalID]
		if slices.Contains(childIDs, id) {
			continue // a collapsed duplicate; the canonical child is already queued
		}
		deps := make([]string, 0, len(st.DependsOn))
		for _, d := range st.DependsOn {
			if rid, ok := localToID[d]; ok {
				deps = append(deps, rid) // sibling reference
			} else {
				deps = append(deps, d) // existing ticket reference
			}
		}
		children = append(children, child{id: id, title: st.Title, key: st.IdempotencyKey, deps: deps, adopt: adopted[id]})
		childIDs = append(childIDs, id)
		if !adopted[id] {
			newEdges[id] = deps // adopted tickets keep their existing edges
		}
	}

	// Governor: bound emergent decomposition depth and per-Goal ticket count.
	if j.depth >= o.opt.MaxGraphDepth {
		o.failDecompose(j, worker, "decomposition depth budget exceeded")
		return
	}
	existing := 0
	for _, t := range s.Tickets {
		if t.GoalID == j.goalID {
			existing++
		}
	}
	added := 0
	for _, c := range children {
		if s.Tickets[c.id] == nil {
			added++
		}
	}
	if added > o.opt.MaxFanOut {
		o.failDecompose(j, worker, "decomposition fan-out budget exceeded")
		return
	}
	if existing+added > o.opt.MaxTicketsPerGoal {
		o.failDecompose(j, worker, "per-goal ticket budget exceeded")
		return
	}

	// Reject dangling dependencies (neither a sibling nor an existing ticket).
	for _, c := range children {
		for _, d := range c.deps {
			if !childSet[d] && s.Tickets[d] == nil {
				o.failDecompose(j, worker, "unknown dependency "+d)
				return
			}
		}
	}

	// Reject decompositions that would deadlock the graph.
	if s.WouldCycle(newEdges) {
		o.failDecompose(j, worker, "decomposition would create a cycle")
		return
	}

	for _, c := range children {
		if c.adopt {
			continue // already exists; it is referenced in Children, not re-created
		}
		_ = o.emit(api.TicketCreated, api.TicketCreatedPayload{
			TicketID:       c.id,
			GoalID:         j.goalID,
			Title:          c.title,
			DependsOn:      c.deps,
			IdempotencyKey: c.key,
			CreatedBy:      worker,
			Depth:          j.depth + 1,
		})
	}
	_ = o.emit(api.TicketDecomposed, api.TicketDecomposedPayload{
		TicketID: j.ticketID, Worker: worker, Children: childIDs, Tokens: tokens, Model: model,
	})
}

// failDecompose terminally fails a parent whose proposed decomposition was
// rejected by a governor or graph check.
func (o *Orchestrator) failDecompose(j dispatchJob, worker, reason string) {
	_ = o.emit(api.TicketFailed, api.TicketFailedPayload{TicketID: j.ticketID, Worker: worker, Reason: reason})
}

// childID builds a stable, hierarchical ticket ID for an emergent child.
func childID(parentID, local string) string { return parentID + "/" + local }

// enforceBudgets is the spend governor (circuit breaker). Any Goal whose tickets
// have spent at least MaxTokensPerGoal tokens stops here: its not-yet-dispatched
// work (Pending/Ready) is failed so no further tokens are spent, while
// already-Proposed/Awaiting work is left to merge (its tokens are already
// charged). The trip is recorded once via GoalBudgetExceeded. Tokens spent in a
// pass that is already in flight may overshoot by up to one dispatch wave — the
// breaker bounds the *next* wave, which is the right granularity for a governor.
func (o *Orchestrator) enforceBudgets(s *state.State) error {
	for _, g := range sortedGoals(s) {
		if g.TokensSpent < o.opt.MaxTokensPerGoal {
			continue
		}
		if !g.BudgetExceeded {
			if err := o.emit(api.GoalBudgetExceeded, api.GoalBudgetExceededPayload{
				GoalID: g.ID, SpentTokens: g.TokensSpent, Limit: o.opt.MaxTokensPerGoal,
			}); err != nil {
				return err
			}
		}
		var doomed []*state.Ticket
		for _, t := range s.Tickets {
			if t.GoalID == g.ID && (t.Status == state.StatusPending || t.Status == state.StatusReady) {
				doomed = append(doomed, t)
			}
		}
		sort.Slice(doomed, func(i, j int) bool { return doomed[i].ID < doomed[j].ID })
		for _, t := range doomed {
			if err := o.emit(api.TicketFailed, api.TicketFailedPayload{
				TicketID: t.ID, Worker: t.Worker, Reason: "goal token budget exceeded",
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

// processProposal verifies and merges a proposed ticket, or rejects it. When
// RequireApproval is set and the ticket is not yet approved, it instead dry-runs
// the Gate and parks the verified candidate for a human decision (ADR 008); the
// real verify+merge happens on a later pass once ApprovalGranted has returned
// the ticket to the queue.
func (o *Orchestrator) processProposal(ctx context.Context, t *state.Ticket) error {
	if o.opt.RequireApproval && !t.Approved {
		out, err := o.mq.DryRun(ctx, mergequeue.Proposal{TicketID: t.ID, Worker: t.Worker, Branch: t.Branch})
		if err != nil {
			return o.rejectOrFail(ctx, t, t.Worker, fmt.Sprintf("merge queue: %v", err))
		}
		if !out.Verified {
			return o.rejectOrFail(ctx, t, t.Worker, out.Reason)
		}
		// Candidate passed the Gate. Record the pass and park for approval; keep
		// the worktree alive so the real merge can reuse the branch.
		if err := o.emit(api.VerificationPassed, api.VerificationPassedPayload{TicketID: t.ID, Worker: t.Worker}); err != nil {
			return err
		}
		return o.emit(api.ApprovalRequested, api.ApprovalRequestedPayload{TicketID: t.ID, Worker: t.Worker, Commit: out.MergeCommit})
	}

	out, err := o.mq.Process(ctx, mergequeue.Proposal{TicketID: t.ID, Worker: t.Worker, Branch: t.Branch})
	if err != nil {
		// Infrastructure failure: treat as a failed attempt.
		return o.rejectOrFail(ctx, t, t.Worker, fmt.Sprintf("merge queue: %v", err))
	}
	return o.applyMergeOutcome(ctx, t, out)
}

// applyMergeOutcome turns a merge-queue Outcome into the right events: a merged
// proposal emits VerificationPassed + Merged (+ RegressionEscaped on a shadow
// miss); a rejected one re-readies or terminally fails the ticket. Shared by the
// single (Process) and batched (ProcessBatch) merge paths.
func (o *Orchestrator) applyMergeOutcome(ctx context.Context, t *state.Ticket, out mergequeue.Outcome) error {
	if out.Merged {
		if err := o.emit(api.VerificationPassed, api.VerificationPassedPayload{TicketID: t.ID, Worker: t.Worker}); err != nil {
			return err
		}
		if err := o.emit(api.Merged, api.MergedPayload{TicketID: t.ID, Worker: t.Worker, Commit: out.MergeCommit}); err != nil {
			return err
		}
		// The Gate accepted the merge but the broader Shadow set caught a
		// regression: record the escape (observational; the merge stands).
		if out.RegressionEscaped {
			if err := o.emit(api.RegressionEscaped, api.RegressionEscapedPayload{TicketID: t.ID, Worker: t.Worker, Reason: out.ShadowReason}); err != nil {
				return err
			}
		}
		o.cleanupWorktree(ctx, t.ID)
		return nil
	}
	return o.rejectOrFail(ctx, t, t.Worker, out.Reason)
}

// pausedForApproval reports whether the run has no actionable work left and is
// only waiting on human approval — the signal for Run to return cleanly.
func pausedForApproval(s *state.State) bool {
	awaiting := 0
	for _, t := range s.Tickets {
		if t.Status.IsTerminal() {
			continue
		}
		if t.Status == state.StatusAwaiting {
			awaiting++
			continue
		}
		return false // some other non-terminal work is still actionable
	}
	return awaiting > 0
}

// rejectOrFail re-readies a ticket for another attempt, or fails it terminally
// once the attempt cap is reached. It also short-circuits a crash loop: the same
// verification failure repeated CrashLoopThreshold times means more attempts
// won't help, so it gives up early instead of burning more tokens (even when
// attempts remain). t reflects state *before* this rejection, so t.SameFailCount
// counts the prior identical failures.
func (o *Orchestrator) rejectOrFail(ctx context.Context, t *state.Ticket, worker, reason string) error {
	if o.opt.CrashLoopThreshold > 0 && reason != "" && reason == t.LastFailReason &&
		t.SameFailCount+1 >= o.opt.CrashLoopThreshold {
		return o.emit(api.TicketFailed, api.TicketFailedPayload{
			TicketID: t.ID, Worker: worker,
			Reason:   fmt.Sprintf("crash loop: %s (×%d)", reason, t.SameFailCount+1),
			Worktree: o.preserveWorktree(t.ID),
		})
	}
	if t.Attempts >= o.opt.MaxAttempts {
		return o.emit(api.TicketFailed, api.TicketFailedPayload{
			TicketID: t.ID, Worker: worker, Reason: reason, Worktree: o.preserveWorktree(t.ID),
		})
	}
	o.cleanupWorktree(ctx, t.ID)
	return o.emit(api.VerificationFailed, api.VerificationFailedPayload{TicketID: t.ID, Worker: worker, Reason: reason})
}

// backoffFor returns how long a ticket must wait before its next attempt:
// exponential in the attempts already made, capped to avoid runaway waits. A
// zero base disables backoff entirely.
func backoffFor(base time.Duration, attempts int) time.Duration {
	if base <= 0 || attempts <= 0 {
		return 0
	}
	shift := attempts - 1
	if shift > 6 {
		shift = 6 // cap at base*64
	}
	return base << shift
}

// dispatchable filters out ready tickets still serving their retry backoff (a
// ticket re-readied by a failure waits backoffFor(attempts) from that failure,
// timestamped as LastActivity). Fresh tickets (no prior attempt) pass straight
// through, so backoff only ever delays retries.
func (o *Orchestrator) dispatchable(ready []*state.Ticket, now time.Time) []*state.Ticket {
	if o.opt.RetryBackoff <= 0 {
		return ready
	}
	out := make([]*state.Ticket, 0, len(ready))
	for _, t := range ready {
		if d := backoffFor(o.opt.RetryBackoff, t.Attempts); d > 0 && now.Sub(t.LastActivity) < d {
			continue
		}
		out = append(out, t)
	}
	return out
}

// nextBackoffWait returns how long until the earliest ready ticket leaves its
// retry backoff, but only when backoff is the *sole* thing blocking progress —
// if any ready ticket is already dispatchable (or there are none), it reports
// false so Run treats a genuine stall as an error rather than sleeping.
func (o *Orchestrator) nextBackoffWait(s *state.State, now time.Time) (time.Duration, bool) {
	if o.opt.RetryBackoff <= 0 {
		return 0, false
	}
	wait := time.Duration(-1)
	for _, t := range s.ReadyTickets() {
		d := backoffFor(o.opt.RetryBackoff, t.Attempts)
		remaining := d - now.Sub(t.LastActivity)
		if d <= 0 || remaining <= 0 {
			return 0, false // something is dispatchable now; not a backoff stall
		}
		if wait < 0 || remaining < wait {
			wait = remaining
		}
	}
	if wait < 0 {
		return 0, false // no ready tickets at all
	}
	return wait, true
}

// failAttempt handles a dispatch-time failure (before a proposal existed). A
// retry cleans up the worktree; a terminal failure preserves it for a warm
// handoff (the human can inspect/take over the agent's attempt).
func (o *Orchestrator) failAttempt(ctx context.Context, j dispatchJob, worker, reason string) {
	if j.attempt >= o.opt.MaxAttempts {
		_ = o.emit(api.TicketFailed, api.TicketFailedPayload{
			TicketID: j.ticketID, Worker: worker, Reason: reason, Worktree: o.preserveWorktree(j.ticketID),
		})
		return
	}
	o.cleanupWorktree(ctx, j.ticketID)
	_ = o.emit(api.WorkerRestarted, api.WorkerRestartedPayload{TicketID: j.ticketID, Worker: worker})
}

func (o *Orchestrator) cleanupWorktree(ctx context.Context, ticketID string) {
	o.mu.Lock()
	wt := o.worktrees[ticketID]
	delete(o.worktrees, ticketID)
	o.mu.Unlock()
	if wt != nil {
		_ = o.repo.Remove(ctx, wt)
	}
}

// preserveWorktree drops the ticket from worktree tracking but leaves the
// checkout on disk, returning its path (empty if none). Used on terminal
// failure so a human can take over the agent's work (warm handoff).
func (o *Orchestrator) preserveWorktree(ticketID string) string {
	o.mu.Lock()
	wt := o.worktrees[ticketID]
	delete(o.worktrees, ticketID)
	o.mu.Unlock()
	if wt != nil {
		return wt.Path
	}
	return ""
}

// --- helpers --------------------------------------------------------------

func (o *Orchestrator) loadState() (*state.State, error) {
	events, err := o.led.Read()
	if err != nil {
		return nil, err
	}
	return state.Fold(events)
}

func (o *Orchestrator) lastSeq() (int, error) {
	s, err := o.loadState()
	if err != nil {
		return 0, err
	}
	return s.LastSeq, nil
}

func (o *Orchestrator) emit(typ api.EventType, payload any) error {
	ev, err := api.NewEvent(typ, "orchestrator", payload)
	if err != nil {
		return err
	}
	_, err = o.led.Append(ev)
	return err
}

func (o *Orchestrator) goalHasTickets(s *state.State, goalID string) bool {
	for _, t := range s.Tickets {
		if t.GoalID == goalID {
			return true
		}
	}
	return false
}

func (o *Orchestrator) allGoalsDecomposed(s *state.State) bool {
	for id := range s.Goals {
		if !o.goalHasTickets(s, id) {
			return false
		}
	}
	return true
}

func sortedGoals(s *state.State) []*state.Goal {
	out := make([]*state.Goal, 0, len(s.Goals))
	for _, g := range s.Goals {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func goalText(s *state.State, t *state.Ticket) string {
	if g := s.Goals[t.GoalID]; g != nil {
		return g.EffectiveText() // includes any mid-run amendments (GoalAmended)
	}
	return ""
}

// detectStalled returns claimed/running Tasks whose last activity predates the
// stall timeout. Pure function for testability (Stall Detector).
func detectStalled(s *state.State, now time.Time, timeout time.Duration) []*state.Ticket {
	var out []*state.Ticket
	for _, t := range s.Tickets {
		if t.Status != state.StatusClaimed && t.Status != state.StatusRunning {
			continue
		}
		if now.Sub(t.LastActivity) > timeout {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ShortID returns 8 hex chars of random entropy, suitable for branch suffixes and goal IDs.
func ShortID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
