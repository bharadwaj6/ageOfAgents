package agent

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
)

// FaultMode is a single injected failure behavior for the chaos tests.
type FaultMode int

const (
	FaultError     FaultMode = iota // return an error (a crashed/erroring worker)
	FaultNoChange                   // edit nothing (an empty proposal)
	FaultConflict                   // edit a shared file to force a merge conflict
	FaultBadVerify                  // write a sentinel that makes the Gate fail post-merge
	FaultCyclic                     // return a cyclic decomposition (must be rejected)
	FaultDuplicate                  // return duplicate-key subtasks (must dedupe to one)
)

// ConflictFile is the path contended writers fight over (forces a merge conflict).
const ConflictFile = "CONTESTED.txt"

// BadVerifyFile is the sentinel a chaos Gate rejects (e.g. `test ! -e FAIL.marker`).
const BadVerifyFile = "FAIL.marker"

// Faulty wraps a Backend and injects randomized faults, seeded for
// reproducibility. It is the fault generator for the Jepsen-style chaos tests:
// whatever faults it injects, the orchestrator's invariants must still hold
// (main stays green, the log replays, no step repetition, the graph stays a DAG,
// and every goal reaches a terminal state).
type Faulty struct {
	Inner Backend
	Modes []FaultMode // enabled fault modes; a normal (delegated) run is always possible

	mu  sync.Mutex
	rng *rand.Rand
}

// NewFaulty wraps inner, injecting the given fault modes under a seeded RNG.
func NewFaulty(inner Backend, seed int64, modes ...FaultMode) *Faulty {
	return &Faulty{Inner: inner, Modes: modes, rng: rand.New(rand.NewSource(seed))}
}

// Name implements Backend.
func (*Faulty) Name() string { return "faulty" }

// pick returns a fault mode, or -1 to delegate to the wrapped backend. It is
// roughly a coin flip between a normal run and a random enabled fault.
func (f *Faulty) pick() FaultMode {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.Modes) == 0 || f.rng.Intn(2) == 0 {
		return -1
	}
	return f.Modes[f.rng.Intn(len(f.Modes))]
}

// Run implements Backend, injecting a fault or delegating to the wrapped backend.
func (f *Faulty) Run(ctx context.Context, task Task) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	switch f.pick() {
	case FaultError:
		return Result{}, fmt.Errorf("faulty: injected error for %q", task.Title)
	case FaultNoChange:
		return Result{Trace: "faulty: no change", Summary: task.Title}, nil
	case FaultConflict:
		// Contended writers all touch the same file with distinct content; the
		// merge queue rejects the conflicting one and main stays green.
		return Result{Trace: "faulty: conflict", Summary: task.Title},
			writeInto(task.Worktree, ConflictFile, task.TicketID+"\n")
	case FaultBadVerify:
		// Write a sentinel the chaos Gate rejects: the merge succeeds, verify
		// fails, the queue rolls back, and main stays green.
		return Result{Trace: "faulty: bad verify", Summary: task.Title},
			writeInto(task.Worktree, BadVerifyFile, "bad\n")
	case FaultCyclic:
		return Result{Subtasks: []Subtask{
			{LocalID: "a", Title: "cyc-a", DependsOn: []string{"b"}, IdempotencyKey: task.TicketID + ":a"},
			{LocalID: "b", Title: "cyc-b", DependsOn: []string{"a"}, IdempotencyKey: task.TicketID + ":b"},
		}}, nil
	case FaultDuplicate:
		// Two subtasks with the same idempotency key must dedupe to one child.
		return Result{Subtasks: []Subtask{
			{LocalID: "x1", Title: "dup", IdempotencyKey: task.TicketID + ":dup"},
			{LocalID: "x2", Title: "dup", IdempotencyKey: task.TicketID + ":dup"},
		}}, nil
	default:
		return f.Inner.Run(ctx, task)
	}
}

func writeInto(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}
