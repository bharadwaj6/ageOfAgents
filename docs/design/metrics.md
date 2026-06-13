# Age of Agents — Success Metrics

How we measure whether the design thesis holds: *a minimal, **Gate-verified** orchestrator coordinates
aligned coding agents more reliably and far more simply than role-hierarchy or market/consensus designs.*

This is an **eval-first** document — define what "working" means before optimizing. It deliberately
drops the metrics that belonged to the rejected design (pheromone convergence, trust discrimination,
stability-horizon rounds) and treats **setup simplicity as a goal, not an anti-goal**.

## Experiment design

Run identical workloads (same repo, Goals, agent model, token budget) and compare. Because the whole
loop is event-sourced, every run is fully reconstructable from `.aoa/events.jsonl` — metrics are computed
by replaying the Event Log, not by bespoke instrumentation.

## Primary metrics

| Metric | What it measures | Target | Why it matters |
|---|---|---|---|
| **Coordination overhead** | LLM tokens spent on coordination (decompose/route/supervise) | **~0** | The Scheduler is deterministic Go; only *work* uses an LLM (ADR 003). |
| **Merge correctness** | % of merges to `main` that keep the Gate green | **100%** | The Gate makes this an invariant, not a hope (ADR 002). |
| **Rejected-Proposal rate** | % of Proposals the Gate rejects before merge | low, and *falling* | A rising rate signals poor decomposition/locality — re-decompose before adding agents. |
| **Step-repetition rate** | duplicate/repeated work units | **~0** | Idempotency Keys make re-runs no-ops; MAST's single largest failure mode (15.7%). |
| **Recovery time** | stall detected → Task re-dispatched | **< 30s** (heartbeat/no-progress timeout) | Deterministic Stall Detector vs. patrol agents. |
| **Throughput per Worker** | merged Tasks / hour / Worker | ≥ baseline | Measures useful work, not raw agent count (Anthropic: more agents ≠ better). |

## Secondary metrics

| Metric | What it measures |
|---|---|
| **Event-replay fidelity** | Can any historical state be reproduced by replaying the Event Log? (yes/no per run) |
| **Single-point-of-failure count** | Components whose failure halts the system. Target **0**: the Scheduler is crash-only and restarts from the durable Event Log. |
| **Mean attempts-to-merge** | Average Task attempts before a successful merge (retry pressure). |

## Simplicity metrics (a goal, not an anti-goal)

The design thesis is that simplicity *is* the feature. Track it explicitly:

| Metric | Target |
|---|---|
| Required external services to run | **0** (git only) |
| Third-party dependencies | minimal (currently 1) |
| Time-to-first-run (`init` → first merged Goal, mock Backend) | seconds, offline |
| Test suite hermeticity | **100% offline** — no API keys, no network (mock Backend) |

## The litmus test

If, on a representative workload, the system cannot show:

1. **Zero coordination LLM sessions** (coordination is deterministic), and
2. **`main` never goes red** (every merge passed the Gate), and
3. **Full event-replay** of any historical state, and
4. **A green, fully offline test suite**,

…then the design has regressed from its own premise. These four are the minimum bar.

## What we explicitly do NOT measure

Pheromone convergence, trust-score discrimination, and stability-horizon round counts — these belonged to
the market/consensus design that was rejected (see `docs/design/adr/005-no-markets-no-consensus.md`).
Correctness comes from an objective Gate, not from agent voting or reputation.
