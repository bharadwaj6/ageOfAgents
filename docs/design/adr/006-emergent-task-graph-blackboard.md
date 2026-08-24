# ADR 006: Emergent Task Graph + Blackboard Coordination

## Status
Accepted

## Context
A purely up-front, central decomposition is both a single point of cognitive failure and an Amdahl
bottleneck. At the same time, direct agent-to-agent messaging
is expensive and error-prone (inter-agent misalignment is 32.3% of MAST failures). Stigmergy's practical
lesson for LLMs is to coordinate through **shared state** (a blackboard), not chat.

## Decision
The task graph is a **living structure on the shared ledger**. A goal seeds initial tickets, but a
worker may emit `TicketCreated` to append new tickets (with `depends_on` edges) at runtime — emergent
decomposition, no central pre-approval and no auctioneer. All coordination is **read/write the shared
event log**; agents never message each other directly. We adopt the **blackboard** form of stigmergy and
explicitly **decline** literal digital-pheromone simulation.

## Consequences
- Decomposition adapts as agents learn about the codebase; no rigid plan to get wrong up front.
- No inter-agent message protocol to get wrong — inter-agent misalignment is 36.9% of MAST failures
  (arXiv:2503.13657), and a shared log removes that surface entirely; great observability.
- Tradeoff: emergent graphs need guards against runaway/duplicate tickets — handled by **idempotency
  keys** ([ADR 010](010-semantic-idempotency.md)) and dependency-readiness gating. Pheromone-style reinforcement is intentionally out.

## Research basis
MAST inter-agent misalignment, 36.9% of failures (arXiv:2503.13657) — the category a shared log removes
as a class. The blackboard lineage (Hearsay-II) and selective communication (STEAM) are classical
multi-agent-systems background, invoked by analogy rather than as evidence for this decision.
