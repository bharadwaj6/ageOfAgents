# ADR 005: No Markets, No Multi-Agent Consensus/Voting for Aligned Coding Agents

## Status
Accepted

## Context
The original vision for this project leaned toward market-based allocation (Contract
Net Protocol, bidding), reputation/trust, and stochastic multi-agent consensus (β-horizons, council
voting). Prior `aoa` implemented all of these. The newer empirical research argues these are the wrong
tools for *aligned, cooperative* coding agents.

## Decision
We **do not build** markets/auctions/strategic bidding, ACO pheromone simulation, multi-agent
debate/voting consensus, or a multi-dimensional trust registry. Task allocation is plain capability/
load-based dispatch (ADR 003); correctness comes from an objective verifier (ADR 002), not agent votes.

## Consequences
- Far less code and conceptual surface; the system is easy to set up, use, validate, and port.
- No "popularity trap," no incentive machinery for a problem (self-interest) we do not have.
- Tradeoff: we forgo emergent specialization-by-bidding and cross-provider council diversity. These are
  revisitable later *only if a measured failure mode demands them* — capability routing (no bidding) and
  best-of-N-with-verifier (no voting) are the natural, narrow re-introductions.

## Research basis
Markets assume self-interested agents, and coding under one goal is pure coordination — Shapley-Coop
(arXiv:2506.07388) frames credit assignment for *self-interested* agents, which is not this case; popularity trap
(arXiv:2510.21513); unreliable self-correction without external feedback (arXiv:2310.01798). This project concludes
game-theoretic primitives are "overkill and often detrimental" for software development.
