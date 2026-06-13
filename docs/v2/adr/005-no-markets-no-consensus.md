# ADR 005: No Markets, No Multi-Agent Consensus/Voting for Aligned Coding Agents

## Status
Accepted

## Context
The original vision (`prompt.md`) and `docs/perplexity.md` lean toward market-based allocation (Contract
Net Protocol, bidding), reputation/trust, and stochastic multi-agent consensus (β-horizons, council
voting). Prior `aoa` implemented all of these. The newer empirical research argues these are the wrong
tools for *aligned, cooperative* coding agents.

## Decision
For v1 we **do not build** markets/auctions/strategic bidding, ACO pheromone simulation, multi-agent
debate/voting consensus, or a multi-dimensional trust registry. Task allocation is plain capability/
load-based dispatch (ADR 003); correctness comes from an objective verifier (ADR 002), not agent votes.

## Consequences
- Far less code and conceptual surface; the system is easy to set up, use, validate, and port.
- No "popularity trap," no incentive machinery for a problem (self-interest) we do not have.
- Tradeoff: we forgo emergent specialization-by-bidding and cross-provider council diversity. These are
  revisitable later *only if a measured failure mode demands them* — capability routing (no bidding) and
  best-of-N-with-verifier (no voting) are the natural, narrow re-introductions.

## Research basis
Markets assume self-interested agents (coding is pure coordination) — `docs/claude.md`; popularity trap
(arXiv:2510.21513); debate ≈ self-consistency (arXiv:2310.01798); `docs/gemini.md` concludes
game-theoretic primitives are "overkill and often detrimental" for software development.
