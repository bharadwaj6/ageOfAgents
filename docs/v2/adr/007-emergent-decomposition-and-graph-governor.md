# ADR 007: Emergent Decomposition Mechanics + Graph Governor

## Status
Accepted

## Context
ADR 006 declared the task graph a *living structure*: a goal seeds initial tickets and workers may
extend it at runtime via `TicketCreated`. But the first implementation only ever seeded **one** ticket
per goal and gave a worker no way to return child work — emergent decomposition was documented, not
built. It also had no guard against the failure modes ADR 006 itself flagged ("emergent graphs need
guards against runaway/duplicate tickets"): nothing rejected a cyclic `depends_on` edge, so a confused
worker could deadlock dependency readiness, and nothing bounded decomposition depth or fan-out.

We need the mechanics for a worker to decompose its ticket, a clean terminal state for the parent, and
the guards — all without violating ADR 003 (no separate LLM coordinator) or ADR 002 (gate-verified merges).

## Decision
A `Backend` `Result` may carry `Subtasks` instead of a code change. When it does, the Scheduler:

1. Resolves each subtask's batch-local handle to a stable child ticket ID, **collapsing duplicate
   idempotency keys** onto one canonical child and **adopting** any child whose key already names a
   ticket (so re-decomposition after a crash is idempotent and never lists a child that was deduped
   away — the phantom-child bug the chaos harness caught).
2. Validates the batch: rejects a missing/duplicate local id, a dangling dependency, a cyclic
   `depends_on` (`state.HasCycle`/`WouldCycle`), or a batch exceeding the governors.
3. On success, emits `TicketCreated` per child (`CreatedBy` = worker, `Depth` = parent+1) and a new
   `TicketDecomposed` event; the parent enters the new terminal status `StatusDecomposed`. A worker
   *either* implements *or* decomposes, never both.

This is a **worker** extending the Shared Log — ADR 003/006 compliant; the Scheduler stays deterministic.

**Graph governor.** Two knobs bound emergent growth, analogous to the concurrency governor:
`MaxGraphDepth` (default 5) and `MaxTicketsPerGoal` (default 64). A decomposition past either fails the
parent terminally.

**Liveness.** `DepsSatisfied` is completion-aware — a `StatusDecomposed` parent is complete once all its
descendants merge. `DeadDependency`/`Blocked` terminally fail any ticket whose dependency can never
complete (a failed dep, or a decomposed subtree containing a dead descendant), so no ticket waits forever.

## Consequences
- Emergent decomposition is real and **provably bounded and acyclic**; the task graph is always a DAG.
- Liveness holds: every goal reaches a terminal state (merged / failed / decomposed).
- Crash-safe and idempotent: re-decomposition after a crash adopts existing children.
- Cost: one new event type, one new terminal status, two governor knobs, and a `Children` field on the
  derived ticket. All derived purely by replay (ADR 001); no side store.
- Verified by the hermetic chaos harness (`internal/invariant`, `internal/agent/faulty.go`,
  `internal/orchestrator/chaos_test.go`), which both asserts the invariants across randomized fault
  histories and is how the duplicate-key phantom-child liveness bug was found and fixed.

## Research basis
LATTE emergent task graphs (`docs/gemini.md`); MAST's Step-Repetition (15.7%) → idempotency keys
(`docs/claude.md`); the distributed-systems framing of failure/liveness guards (`docs/claude.md`).
Builds directly on ADR 001 (event-sourced truth), ADR 003 (flat deterministic Scheduler), and ADR 006
(emergent graph + blackboard).
