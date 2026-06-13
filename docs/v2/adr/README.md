# Architecture Decision Records — Age of Agents v2

These ADRs record the load-bearing decisions for the v2 orchestrator. Each is grounded in the research
corpus under `docs/` (`claude.md`, `gemini.md`, `grok.md`, `perplexity.md`, `research_links.md`).

| ADR | Decision |
|-----|----------|
| [001](001-event-sourced-truth.md) | Event-sourced truth, not a mutable store |
| [002](002-verifier-gated-merge-queue.md) | A serializing merge queue gated by an objective verifier |
| [003](003-flat-orchestrator-worker.md) | Flat orchestrator–worker + work-stealing, one deterministic reconciler |
| [004](004-pluggable-agent-backend.md) | Pluggable agent backend (mock + claudecode) behind one interface |
| [005](005-no-markets-no-consensus.md) | No markets, no multi-agent consensus/voting for aligned coding agents |
| [006](006-emergent-task-graph-blackboard.md) | Emergent task graph + blackboard coordination (shared state, not messaging) |
