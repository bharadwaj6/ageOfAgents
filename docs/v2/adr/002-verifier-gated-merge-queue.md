# ADR 002: A Serializing Merge Queue Gated by an Objective Verifier

## Status
Accepted

## Context
`main` must stay correct while many workers produce changes in parallel. The dominant failure cluster in
multi-agent coding is verification (MAST Task Verification 23.5%; adding objective verification yields
~+15.6% success). Agent-judged correctness is unreliable: multi-agent debate ≈ self-consistency at equal
cost (Huang et al., arXiv:2310.01798), and consensus voting hits a "popularity trap" (Vallecillos-Ruiz
et al., arXiv:2510.21513).

## Decision
Every proposal passes through a **single serializing merge queue**. The queue runs a configured
**objective verifier** (build / tests / lint) and merges to `main` **only if it passes**, giving
linearizable, always-green `main`. Selection among alternatives (best-of-N) uses the verifier, never
majority vote. The queue serializes *only writes to `main`* — it does **not** block worker dispatch.

## Consequences
- `main` is always verifier-green; bad changes never land.
- Correctness rests on an objective signal, not agent opinion.
- Tradeoff: strict serialization is a potential tail-straggler latency point at high parallelism;
  mitigated by decoupling from dispatch now, and (later) batching non-conflicting proposals.

## Research basis
MAST verification findings (arXiv:2503.13657); popularity trap (arXiv:2510.21513); debate ≈
self-consistency (arXiv:2310.01798); linearizability (Kleppmann, *DDIA*).
