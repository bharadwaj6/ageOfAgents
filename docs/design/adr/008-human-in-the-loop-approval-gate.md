# ADR 008: Optional Human-in-the-Loop Approval Gate

## Status
Accepted

## Context
The Gate (ADR 002) makes correctness objective: nothing merges unless build/tests/lint pass. That is
the right default and stays the default. But for some goals — production-touching changes, schema or data
migrations, changes whose correctness the existing tests do not fully capture — an operator wants a final
human checkpoint before code reaches `main`. External review of the design flagged this gap: the Gate is
extensible in *what it runs* (it is an ordered list of commands), but it had no notion of a *human*
decision in the loop.

The hard constraint: adding a human checkpoint must not weaken any existing invariant — in particular
`main` must never sit half-merged while a person deliberates, and a merge must still imply a passing Gate
(ADR 002) and replay determinism (ADR 001). It must also not put an LLM in the coordination path (ADR
003).

## Decision
Add an **optional** approval gate, off by default (`require_approval` in `aoa.toml`). When enabled, a
verified proposal is **parked for a human decision** before it merges:

1. The merge queue grows a `DryRun` that merges the candidate, runs the Gate against the post-merge state,
   and **always rolls `main` back** — reporting whether the proposal *would* merge cleanly without ever
   writing to `main`.
2. When approval is required, the Scheduler dry-runs the proposal; on a pass it emits `VerificationPassed`
   + `ApprovalRequested` and parks the ticket in a new non-terminal `awaiting` state (the worktree is kept
   alive). On a dry-run failure it rejects/retries exactly as today.
3. A human records the decision via `aoa approve <ticket>` / `aoa reject <ticket>`, which append
   `ApprovalGranted` / `ApprovalDenied` to the log. Approval returns the ticket to the queue flagged
   approved; the queue then does the **real** verify+merge (re-checking the Gate, so a stale candidate is
   caught). Denial fails the ticket terminally.
4. `Run` returns cleanly when the only unsettled work is awaiting approval, so approval is asynchronous: a
   later `aoa approve` + `aoa run` resumes the merge.

A new invariant, **`ApprovalGate`**, asserts that any ticket for which approval was requested cannot be
`Merged` without an intervening `ApprovalGranted` — enforced regardless of config.

## Consequences
- Operators get a hard human checkpoint without sacrificing any invariant: a pending approval leaves
  `main` untouched (dry-run rolls back), and the real merge re-verifies the Gate.
- Coordination stays deterministic Go and event-sourced — approval is just three new events folded into
  state; no LLM and no side store (ADR 001, 003).
- Default behavior is unchanged: with `require_approval = false` the merge path is exactly as before.
- Tradeoff: a dry-run costs one extra merge+verify+rollback per parked proposal. Acceptable — it is the
  price of presenting a Gate-verified candidate to the human.

## Research basis
MAST Task-Verification findings motivate stronger, not weaker, verification (arXiv:2503.13657); the
modality-shift principle says the strongest checks are objective execution — the human gate *adds* to that
signal rather than replacing it. Single-writer linearizability for `main` is preserved (Kleppmann, *DDIA*).
