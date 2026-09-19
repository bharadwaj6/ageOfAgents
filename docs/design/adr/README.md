# Architecture Decision Records — Age of Agents

These ADRs record the load-bearing decisions for the orchestrator.

Where a decision rests on external evidence, the ADR cites the paper directly and the
[reading list](../reading-list.md) says which claim each source supports. Some decisions rest on
ordinary engineering practice rather than research, and say so.

| ADR | Decision |
|-----|----------|
| [001](001-event-sourced-truth.md) | Event Log is truth, not a mutable store |
| [002](002-verifier-gated-merge-queue.md) | A serializing Merge Queue gated by an objective Gate |
| [003](003-flat-orchestrator-worker.md) | Flat Scheduler–Worker topology, one deterministic control loop |
| [004](004-pluggable-agent-backend.md) | Pluggable agent Backend behind one interface (extended by 014) |
| [005](005-no-markets-no-consensus.md) | No markets, no multi-agent consensus/voting for aligned coding agents |
| [006](006-emergent-task-graph-blackboard.md) | Emergent Task Graph + Shared Log coordination (not messaging) |
| [007](007-emergent-decomposition-and-graph-governor.md) | Emergent decomposition mechanics + cycle/depth/fan-out governor |
| [008](008-human-in-the-loop-approval-gate.md) | Optional human-in-the-loop approval gate (dry-run + approve/reject) |
| [009](009-live-evaluation-out-of-hermetic-suite.md) | Backend-agnostic live evaluation harness, opt-in and outside the hermetic suite |
| [010](010-semantic-idempotency.md) | Semantic idempotency via worker-supplied keys (identity, not output) |
| [011](011-debate-markets-as-offline-tools.md) | Debate/voting/markets rejected as a live control plane, not universally |
| [012](012-observability-as-replay-projection.md) | Observability is a replay projection to OTLP (OpenTelemetry), off by default |
| [013](013-worker-pool-not-dispatch-wave.md) | Dispatch is an asynchronous worker pool, not a per-pass wave |
| [014](014-cli-backends-as-data.md) | A CLI backend is a table row, and the table is reachable from `aoa.toml` |
| [015](015-aoa-is-a-backend.md) | `aoa` is a backend: front doors decide what, `aoa` decides whether it lands (extends 003, 008, 010) |
| [016](016-deliver-a-goal-as-a-pull-request.md) | Deliver a Goal as one pull request from a Gate-verified branch; the forge owns landing (extends 002, 015) |
| [017](017-spend-is-bounded-before-it-happens.md) | Budgets are enforced before work starts, from the log: harness-reported cost, per attempt/goal/run/day; automation runs only on a budget (extends 015, 016; resolves #77 per workspace) |
