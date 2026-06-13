// Package orchestrator is the single deterministic reconciler that drives the
// whole loop (docs/v2/adr/003-flat-orchestrator-worker.md):
//
//	observe(ledger) -> fold -> diff desired vs actual -> act -> append events
//
// One controller, not eleven. It decomposes goals, promotes dependency-ready
// tickets, dispatches workers under a concurrency governor, drives the
// verifier-gated merge queue, and restarts stalled workers. All coordination
// is plain Go (no LLM); only the work itself is done by the agent backend.
package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
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
	Concurrency  int           // max workers in flight (governor); default 4
	MaxAttempts  int           // attempts per ticket before failing; default 2
	Conventions  string        // injected into every agent prompt
	WorktreeBase string        // where per-ticket worktrees live; default <repo>/.git/aoa-worktrees
	StallTimeout time.Duration // no-progress timeout for the failure detector; default 2m
	MaxPasses    int           // safety bound on reconcile passes in Run; default 1000
	Now          func() time.Time
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
	if opt.WorktreeBase == "" {
		opt.WorktreeBase = filepath.Join(repo.Dir, ".git", "aoa-worktrees")
	}
	if opt.Now == nil {
		opt.Now = time.Now
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
	ready := s.ReadyTickets()
	n := min(slots, len(ready))
	if n > 0 {
		var wg sync.WaitGroup
		for _, t := range ready[:n] {
			job := dispatchJob{
				ticketID: t.ID,
				title:    t.Title,
				goalText: goalText(s, t),
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
	if s, err = o.loadState(); err != nil {
		return err
	}
	for _, t := range s.Proposed() {
		if err := o.processProposal(ctx, t); err != nil {
			return err
		}
	}

	// 5. Failure detector: restart workers with no recent progress.
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
	title    string
	goalText string
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

	branch := "aoa/" + worktree.SanitizeBranch(j.ticketID) + "-" + newID()
	dest := filepath.Join(o.opt.WorktreeBase, worktree.SanitizeBranch(branch))
	wt, err := o.repo.AddWorktree(ctx, dest, branch)
	if err != nil {
		o.failAttempt(j, worker, fmt.Sprintf("worktree: %v", err))
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
		o.cleanupWorktree(ctx, j.ticketID)
		o.failAttempt(j, worker, fmt.Sprintf("agent: %v", err))
		return
	}

	sha, changed, err := wt.Commit(ctx, fmt.Sprintf("feat: %s (%s)", j.title, j.ticketID))
	if err != nil {
		o.cleanupWorktree(ctx, j.ticketID)
		o.failAttempt(j, worker, fmt.Sprintf("commit: %v", err))
		return
	}
	if !changed {
		o.cleanupWorktree(ctx, j.ticketID)
		o.failAttempt(j, worker, "agent produced no changes")
		return
	}

	_ = o.emit(api.ProposalSubmitted, api.ProposalSubmittedPayload{
		TicketID: j.ticketID, Worker: worker, Branch: branch, Commit: sha, Trace: res.Trace,
	})
}

// processProposal verifies and merges a proposed ticket, or rejects it.
func (o *Orchestrator) processProposal(ctx context.Context, t *state.Ticket) error {
	out, err := o.mq.Process(ctx, mergequeue.Proposal{TicketID: t.ID, Worker: t.Worker, Branch: t.Branch})
	if err != nil {
		// Infrastructure failure: treat as a failed attempt.
		o.cleanupWorktree(ctx, t.ID)
		return o.rejectOrFail(t, t.Worker, fmt.Sprintf("merge queue: %v", err))
	}

	if out.Merged {
		if err := o.emit(api.VerificationPassed, api.VerificationPassedPayload{TicketID: t.ID, Worker: t.Worker}); err != nil {
			return err
		}
		if err := o.emit(api.Merged, api.MergedPayload{TicketID: t.ID, Worker: t.Worker, Commit: out.MergeCommit}); err != nil {
			return err
		}
		o.cleanupWorktree(ctx, t.ID)
		return nil
	}

	o.cleanupWorktree(ctx, t.ID)
	return o.rejectOrFail(t, t.Worker, out.Reason)
}

// rejectOrFail re-readies a ticket for another attempt, or fails it terminally
// once the attempt cap is reached.
func (o *Orchestrator) rejectOrFail(t *state.Ticket, worker, reason string) error {
	if t.Attempts >= o.opt.MaxAttempts {
		return o.emit(api.TicketFailed, api.TicketFailedPayload{TicketID: t.ID, Worker: worker, Reason: reason})
	}
	return o.emit(api.VerificationFailed, api.VerificationFailedPayload{TicketID: t.ID, Worker: worker, Reason: reason})
}

// failAttempt handles a dispatch-time failure (before a proposal existed).
func (o *Orchestrator) failAttempt(j dispatchJob, worker, reason string) {
	if j.attempt >= o.opt.MaxAttempts {
		_ = o.emit(api.TicketFailed, api.TicketFailedPayload{TicketID: j.ticketID, Worker: worker, Reason: reason})
		return
	}
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
		return g.Text
	}
	return ""
}

// detectStalled returns claimed/running tickets whose last activity predates the
// stall timeout. Pure function for testability.
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

func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
