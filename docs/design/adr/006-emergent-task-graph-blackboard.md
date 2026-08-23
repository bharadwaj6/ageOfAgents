# ADR 006: Emergent Task Graph + Blackboard Coordination

## Status
Accepted

## Context
A purely up-front, central decomposition is both a single point of cognitive failure and an Amdahl
bottleneck (`docs/research/gemini.md` critique, citing LATTE). At the same time, direct agent-to-agent messaging
is expensive and error-prone (inter-agent misalignment is 32.3% of MAST failures). Stigmergy's practical
lesson for LLMs (`docs/research/grok.md`) is to coordinate through **shared state** (a blackboard), not chat.

## Decision
The task graph is a **living structure on the shared ledger**. A goal seeds initial tickets, but a
worker may emit `TicketCreated` to append new tickets (with `depends_on` edges) at runtime — emergent
decomposition, no central pre-approval and no auctioneer. All coordination is **read/write the shared
event log**; agents never message each other directly. We adopt the **blackboard** form of stigmergy and
explicitly **decline** literal digital-pheromone simulation.

## Consequences
- Decomposition adapts as agents learn about the codebase; no rigid plan to get wrong up front.
- ~80% less coordination overhead than direct messaging (per `docs/research/grok.md`); great observability.
- Tradeoff: emergent graphs need guards against runaway/duplicate tickets — handled by **idempotency
  keys** (ADR 001) and dependency-readiness gating. Pheromone-style reinforcement is intentionally out.

## Research basis
LATTE emergent task graphs (`docs/research/gemini.md`); stigmergy/blackboard synthesis (`docs/research/grok.md`);
Hearsay-II blackboard lineage and selective communication (STEAM) via `docs/research/claude-report.md`.
