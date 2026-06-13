# Age of Agents v2 — Success Metrics

How we measure whether the v2 thesis holds: *a minimal, **verifier-gated** orchestrator coordinates
aligned coding agents more reliably and far more simply than role-hierarchy or market/consensus designs.*

This is an **eval-first** document — define what "working" means before optimizing. It deliberately
drops the metrics that belonged to the rejected design (pheromone convergence, trust discrimination,
stability-horizon rounds) and treats **setup simplicity as a goal, not an anti-goal**.

## Experiment design

Run identical workloads (same repo, goals, agent model, token budget) and compare. Because the whole
loop is event-sourced, every run is fully reconstructable from `.aoa/events.jsonl` — metrics are computed
by folding the log, not by bespoke instrumentation.

## Primary metrics

| Metric | What it measures | Target | Why it matters |
|---|---|---|---|
| **Coordination overhead** | LLM tokens spent on coordination (decompose/route/supervise) | **~0** | The reconciler is deterministic Go; only *work* uses an LLM (ADR 003). |
| **Merge correctness** | % of merges to `main` that keep the verifier green | **100%** | The gate makes this an invariant, not a hope (ADR 002). |
| **Rejected-proposal rate** | % of proposals the verifier rejects before merge | low, and *falling* | A rising rate signals poor decomposition/locality — re-decompose before adding agents. |
| **Step-repetition rate** | duplicate/repeated work units | **~0** | Idempotency keys make re-runs no-ops; MAST's single largest failure mode (15.7%). |
| **Recovery time** | stall detected → ticket re-dispatched | **< 30s** (heartbeat/no-progress timeout) | Deterministic failure detection vs. patrol agents. |
| **Throughput per worker** | merged tickets / hour / worker | ≥ baseline | Measures useful work, not raw agent count (Anthropic: more agents ≠ better). |

## Secondary metrics

| Metric | What it measures |
|---|---|
| **Event-replay fidelity** | Can any historical state be reproduced by replaying the log? (yes/no per run) |
| **Single-point-of-failure count** | Components whose failure halts the system. Target **0**: the reconciler is crash-only and restarts from the durable log. |
| **Mean attempts-to-merge** | Average ticket attempts before a successful merge (retry pressure). |

## Simplicity metrics (a goal, not an anti-goal)

The v2 thesis is that simplicity *is* the feature. Track it explicitly:

| Metric | Target |
|---|---|
| Required external services to run | **0** (git only) |
| Third-party dependencies | minimal (currently 1) |
| Time-to-first-run (`init` → first merged goal, mock backend) | seconds, offline |
| Test suite hermeticity | **100% offline** — no API keys, no network (mock backend) |

## The v2 litmus test

If, on a representative workload, the system cannot show:

1. **Zero coordination LLM sessions** (coordination is deterministic), and
2. **`main` never goes red** (every merge passed the objective verifier), and
3. **Full event-replay** of any historical state, and
4. **A green, fully offline test suite**,

…then the design has regressed from its own premise. These four are the minimum bar.

## What we explicitly do NOT measure

Pheromone convergence, trust-score discrimination, and stability-horizon round counts — these belonged to
the market/Byzantine-consensus design that v2 rejected (see `docs/v2/adr/005-no-markets-no-consensus.md`).
Correctness comes from an objective verifier, not from agent voting or reputation.
