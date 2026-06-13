# Formal model — Age of Agents

[`Orchestrator.tla`](Orchestrator.tla) is a small TLA+ model of the load-bearing part of the design: the
**single-writer, Gate-verified merge queue** with **idempotency keys** and the optional
**human-in-the-loop approval gate** (ADRs 002 and 008). It exists so the invariants the Go suite checks
*dynamically* (over event histories, in `internal/invariant`) are also pinned down *statically*, by
exhaustive model checking — and so they stay pinned when the merge path changes (e.g. speculative
batching later).

## Invariants proven

| TLA+ invariant | Go counterpart (`internal/invariant`) | Meaning |
|---|---|---|
| `MergeImpliesVerified` | `MergeImpliesVerified` / `MainGreen` (I1) | nothing merges unless the Gate passed → `main` is never red |
| `MergedAtMostOnce` | `MergedAtMostOnceByQueue` (I2) | single writer; a ticket merges at most once |
| `NoDuplicateMergedKey` | `NoDuplicateMergedKey` (I4) | no two merged tickets share an idempotency key (no step repetition) |
| `ApprovalGate` | `ApprovalGate` (ADR 008) | with approval on, nothing merges without explicit approval |
| `TypeOK` | — | state/type well-formedness |

## Checking it

Needs Java and `tla2tools.jar` (TLC):

```bash
java -cp tla2tools.jar tlc2.TLC -config Orchestrator.cfg Orchestrator.tla
# => "Model checking completed. No error has been found."
```

The committed model checks 3 tickets (t1 and t2 share an idempotency key, t3 is distinct) with the
approval gate enabled — 518 distinct states, all invariants hold. The scenario is defined inline at the
top of the module (`Tickets`, `Key`, `RequireApproval`); edit those to explore other shapes (more
tickets, distinct keys, or `RequireApproval == FALSE` to model the default merge path).

Liveness (every ticket eventually reaches a terminal state) is intentionally **not** modeled here — it is
asserted dynamically by the Go suite (`invariant.Settled`), so terminal states are configured as
legitimate (`CHECK_DEADLOCK FALSE`) rather than flagged as deadlocks.
