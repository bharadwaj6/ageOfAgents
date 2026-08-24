# ADR 010: Semantic Idempotency via Worker-Supplied Keys

## Status
Accepted (documents existing, load-bearing behavior)

## Context
Step repetition is the single largest MAST failure mode (FM-1.3, 15.7%): agents redo completed work,
especially after a crash, retry, or duplicated dispatch. The design's answer is "idempotency by
construction" (architecture.md §2.3), but external review noted the *semantic* story — what happens when a
stochastic agent is re-run and produces a slightly different output — was never written down. This ADR
records the mechanism that already exists in the code so it is a documented contract, not folklore.

## Decision
Every unit of work carries an **idempotency key**, and identity is by key, not by output:

- A worker proposing emergent children supplies an `IdempotencyKey` per `Subtask`
  (`internal/agent/agent.go`). The root ticket of a goal uses `<goalID>:impl`.
- `state.Apply` dedupes on the key: a second `TicketCreated` for a key already mapped to a ticket is a
  **no-op** (`internal/state/state.go`, `KeyToTicket`). The Event Log can therefore carry redundant
  creation events without creating phantom tickets.
- The Scheduler resolves a re-proposed child whose key already names a ticket by **adopting** the existing
  ticket rather than creating a new one (`orchestrator.decompose` via `state.TicketForKey`). This is what
  makes re-decomposition after a crash safe: the same logical graph collapses onto the same tickets.
- Duplicate keys *within a single decomposition batch* collapse to one canonical child, so the emitted
  `Children` list never references a ticket that was deduped away (a liveness bug the chaos harness found
  and this rule fixed).
- The invariant `NoDuplicateMergedKey` (I4) asserts no two distinct merged tickets ever share a key, and
  the TLA+ model (`docs/design/formal`) proves it exhaustively for the merge path.

The key insight on *output* divergence: the system does not try to make a stochastic agent deterministic.
Idempotency is enforced at the **task-identity** layer — a logical task with a given key is merged **at
most once**. A retry that produces a different diff is still one merge for that key (or, if the first
already merged, the re-dispatch is adopted/short-circuited). Correctness of whichever diff lands is the
Gate's job (ADR 002), not idempotency's.

## Consequences
- Crash recovery, duplicated dispatch, and re-decomposition are no-ops at the application layer — directly
  attacking the #1 MAST failure mode.
- Keys are part of the worker contract: the shared agent prompt (`agent.BuildPrompt`) asks for an `idempotency_key` per subtask,
  and a worker that omits stable keys forfeits adoption (it may create siblings). Stable, goal-scoped keys
  (`<goal>:<component>`) are the documented convention.
- This is a behavioral contract over the existing code; no code change accompanies this ADR.

## Research basis
MAST step-repetition findings (arXiv:2503.13657); idempotency/exactly-once processing as a state-identity
property, not an output-determinism property (Kleppmann, *DDIA*).
