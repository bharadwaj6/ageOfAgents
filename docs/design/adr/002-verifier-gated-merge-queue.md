# ADR 002: A Serializing Merge Queue Gated by an Objective Verifier

## Status
Accepted

## Context
`main` must stay correct while many workers produce changes in parallel. Task Verification is one of the
three MAST failure categories at 21.3% (Cemri et al., arXiv:2503.13657) — not the largest, but the one a
control plane can act on directly, since System Design and Inter-Agent Misalignment are addressed by the
topology (ADR 003) and the shared log (ADR 006) rather than by a gate.

Agent-judged correctness is the alternative, and it is unreliable: LLMs "struggle to self-correct their
responses without external feedback" (Huang et al., arXiv:2310.01798), and consensus voting hits a
"popularity trap" that amplifies common but incorrect outputs (Vallecillos-Ruiz et al.,
arXiv:2510.21513).

## Decision
Every proposal passes through a **single serializing merge queue**. The queue runs a configured
**objective verifier** (build / tests / lint) and merges to `main` **only if it passes**, giving
linearizable, always-green `main`. Selection among alternatives (best-of-N) uses the verifier, never
majority vote. The queue serializes *only writes to `main`* — it does **not** block worker dispatch.

## Consequences
- `main` is always verifier-green; bad changes never land.
- Correctness rests on an objective signal, not agent opinion.
- Tradeoff: strict serialization is a potential tail-straggler latency point at high parallelism;
  mitigated by decoupling from dispatch (ADR 013) and by batching proposals that touch disjoint files
  (`mergequeue.ProcessBatch`), with a serial fallback when the file sets overlap.

## Research basis
MAST verification findings (arXiv:2503.13657); popularity trap (arXiv:2510.21513); unreliable
self-correction without external feedback ≈
self-consistency (arXiv:2310.01798); linearizability (Kleppmann, *DDIA*).
