# ADR 011: Debate / Voting / Markets — Rejected as a Live Control Plane, Not Universally

## Status
Accepted (refines ADR 005)

## Context
ADR 005 rejects markets, consensus voting, and multi-agent debate, and the architecture documents say so
forcefully. External review agreed with the decision for *this* system but flagged the wording as
over-broad: the cited literature rejects these mechanisms as a **live coordination/selection** layer for
aligned coding agents, not as techniques with no use anywhere. Notably, multi-agent debate is a useful
**offline** technique — for generating training signal or exploring a design space away from the merge
path — even though it is a poor *runtime* coordinator. This ADR records that distinction so the claim is precise and ages
well.

## Decision
Keep the rejection, but scope it explicitly to the **live control plane**:

- **In `aoa`'s runtime, correctness comes from the objective Gate**, never from agent debate, majority
  voting, reputation, or bidding. This is unchanged (ADR 002, 005): debate ≈ self-consistency at equal
  cost (arXiv:2310.01798); voting hits a popularity trap (arXiv:2510.21513); markets assume self-interested
  agents whereas aligned coding is a pure coordination problem.
- **We do not claim these techniques are worthless in general.** Off the critical path — for *offline*
  data generation, model training/distillation, or research evaluation — debate and ensembling can produce
  useful signal. That is simply out of scope for a deterministic, Gate-verified orchestrator and would
  enter, if ever, as an offline tool that emits ordinary `agent.Backend` work, never as a second
  coordinator or a runtime selector.

## Consequences
- The architecture's "what we deliberately do NOT build" stance is preserved and now precisely worded:
  rejected *as live coordination*, not *as ideas*.
- No code change. Architecture.md §7 and comparison.md are reworded to reflect the scoping.

## Research basis
Debate ≈ self-consistency (arXiv:2310.01798); ensemble popularity trap (arXiv:2510.21513); MAST
(arXiv:2503.13657). The offline-use carve-out is a scoping statement, not a claim backed by a source
here.
