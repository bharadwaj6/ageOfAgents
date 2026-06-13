# ADR 003: Flat Orchestrator–Worker + Work-Stealing, One Deterministic Reconciler

## Status
Accepted

## Context
Gas Town and prior `aoa` used role hierarchies (Mayor/Witness/Deacon) or many controllers (eleven, in
`main`). Anthropic's multi-agent work shows the **flat orchestrator–worker** topology is the simplest
sound default; markets/leader-election add complexity that aligned coding agents do not need.

## Decision
A **single deterministic reconciler** (`internal/orchestrator`) runs the loop `observe → fold → diff →
act`. Idle worker capacity **pulls** the next dependency-ready ticket (work-stealing); there is no
"Mayor" pushing assignments and no role personas. A **concurrency governor** caps in-flight workers
(backpressure). The reconciler is plain Go (sub-millisecond) — coordination cannot hallucinate and is
not a cognitive bottleneck. Recovery is a **single failure detector + crash-only restart** from the
durable log (replacing the Witness/Deacon/Dogs watchdog cast).

## Consequences
- One small, testable, deterministic control component instead of a role hierarchy.
- Work self-balances; adding/removing workers is trivial.
- Tradeoff: weaker for *tightly coupled* tasks — mitigated by decomposing for locality and by the
  emergent task graph (ADR 006). Multi-node leader election is deferred until actually needed.

## Research basis
Anthropic orchestrator–worker pattern; Kubernetes reconciliation loops; actor-model "let it crash"
supervision; control theory (governor as backpressure). See `docs/claude.md`.
