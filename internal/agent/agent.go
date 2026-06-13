// Package agent defines the single seam between the Scheduler and the AI
// coding agent (docs/v2/adr/004-pluggable-agent-backend.md). Business logic
// never calls a provider SDK directly; it goes through [Backend].
//
// A Backend's job is narrow: given a [Task], make the necessary file changes in
// the Task's isolated worktree and return a [Result] (a reasoning trace). The
// Scheduler is responsible for committing those changes to a branch and for
// verifying/merging them — the Backend does not touch git or the Merge Queue.
package agent

import "context"

// Task is a single unit of work handed to a Backend.
type Task struct {
	TicketID    string // stable Task identifier
	Title       string // what to do
	Goal        string // the parent Goal text, for context
	Worktree    string // absolute path to the isolated git worktree to edit
	Conventions string // project Conventions, injected as shared coding rules
}

// Subtask is a child unit of work a Backend proposes when it decides a Task is
// too large to implement directly (emergent decomposition, ADR 006). The
// Scheduler turns each Subtask into a TicketCreated event on the Shared Log;
// agents never message each other. A Result carrying Subtasks is a
// *decomposition*: the Backend should not also edit the worktree.
type Subtask struct {
	// LocalID is a batch-local handle other Subtasks reference in DependsOn.
	// The Scheduler resolves it to a stable, globally-unique ticket ID.
	LocalID string
	// Title is what the child ticket should accomplish.
	Title string
	// DependsOn lists sibling LocalIDs and/or existing ticket IDs that must
	// merge before this child becomes ready.
	DependsOn []string
	// IdempotencyKey makes re-proposing the same logical child a no-op.
	IdempotencyKey string
}

// Result is what a Backend returns after handling a Task. A Backend either
// edits the worktree (an implementation) or returns Subtasks (a decomposition),
// not both.
type Result struct {
	Trace    string    // short reasoning trace for the audit log
	Summary  string    // one-line summary of the change
	Subtasks []Subtask // non-empty => decompose this Task into children
}

// Backend executes coding work for a single task. Implementations must be safe
// to call from multiple goroutines for distinct tasks.
type Backend interface {
	// Name identifies the backend (e.g. "mock", "claudecode").
	Name() string
	// Run performs the task's work inside task.Worktree. It should be
	// cancellable via ctx and must not assume any shared state with other tasks.
	Run(ctx context.Context, task Task) (Result, error)
}
