# ADR 009: Live Evaluation Lives Outside the Hermetic Suite

## Status
Accepted

## Context
Every headline number `aoa` reports — 0 coordination LLM sessions, 100% merge correctness, 0 MAST
failure modes — is produced by the deterministic `mock` Backend (ADR 004). That proves the *coordination
machinery* is correct, but says nothing about real task-success when an actual agent does the work.
External review identified this mock→live gap as the single largest credibility gap. Closing it requires
running real LLMs against real repositories, which means network access, API keys, cost, and
non-determinism — none of which may leak into the default test suite, whose hermeticity is a
non-negotiable (CLAUDE.md / AGENTS.md).

## Decision
Add a **backend-agnostic** end-to-end evaluation harness (`internal/liveeval`, surfaced as `aoa eval
--tasks <file> [--backend mock|claudecode]`) and keep it cleanly separated from the hermetic guarantees:

- `liveeval.Run` takes an `agent.Backend` and a prepared git repo, runs the orchestrator to completion,
  and scores the result against a configurable **success oracle** (commands that must pass on the final
  `main`, e.g. an issue's reproduce test). All metrics, the MAST histogram, and invariant checks are
  derived by replaying the run's Event Log — the same replay-only discipline as `bench`/`metrics`.
- The harness reaches the network only if the caller passes a networked Backend. The default `go test`
  suite exercises it **only with the `mock` Backend** against a temp repo, so it stays hermetic.
- Live runs are explicitly opt-in: `--backend claudecode` requires the agent binary, API keys, and
  network. They are never part of `go test ./...` and their numbers are reported separately, not folded
  into the hermetic claims.
- Token/cost accounting is event-sourced: `agent.Result.Tokens` flows onto `ProposalSubmitted` /
  `TicketDecomposed` payloads and is summed by `metrics.TokensTotal`. The `mock` reports 0; `claudecode`
  reports best-effort usage from an optional `aoa:usage` block.

## Consequences
- The project can finally produce live, comparable evidence (task success, tokens, wall-clock, rollback
  rate, MAST profile) without weakening the hermetic suite or adding a required external service.
- The bench (hermetic, mock) and eval (opt-in, live) stay clearly distinct: one proves correctness by
  construction, the other measures real-world efficacy. Neither is presented as the other.
- Tradeoff: `liveeval` assumes the caller has prepared the repo (clone/checkout). Repo provisioning and a
  specific benchmark adapter (e.g. SWE-bench Lite) are left to the caller/scripts, keeping the package
  small and dependency-free.

## Research basis
Live coding-agent evaluation practice (SWE-bench / SWE-bench Lite, arXiv:2310.06770); the design's own
thesis that an objective Gate is the success signal (ADR 002) — here generalized into a per-task success
oracle. VCR-style hermeticity for the default suite remains the rule (ADR 004).
