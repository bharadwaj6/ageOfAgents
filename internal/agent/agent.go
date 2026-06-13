// Package agent defines the single seam between the orchestrator and the LLM
// coding agent (docs/v2/adr/004-pluggable-agent-backend.md). Business logic
// never calls a provider SDK directly; it goes through [Backend].
//
// A Backend's job is narrow: given a [Task], make the necessary file changes in
// the task's isolated worktree and return a [Result] (a reasoning trace). The
// orchestrator is responsible for committing those changes to a branch and for
// verifying/merging them — the Backend does not touch git or the merge queue.
package agent

import "context"

// Task is a single unit of work handed to a Backend.
type Task struct {
	TicketID    string // stable ticket identifier
	Title       string // what to do
	Goal        string // the parent goal text, for context
	Worktree    string // absolute path to the isolated git worktree to edit
	Conventions string // project conventions, injected as shared "Schelling points"
}

// Result is what a Backend returns after editing the worktree.
type Result struct {
	Trace   string // short reasoning trace for the audit log
	Summary string // one-line summary of the change
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
