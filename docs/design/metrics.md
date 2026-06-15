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

## MAST failure-mode observability

Beyond aggregate metrics, every run can be scored directly against the **MAST taxonomy**
(Cemri et al., *Why Do Multi-Agent LLM Systems Fail?*, arXiv:2503.13657). `internal/diagnose`
classifies the Event Log into a per-mode histogram — turning "the design is *aligned* with MAST"
into "this run is *measured* against MAST". Like every other metric, it is a pure function of the
log (no bespoke instrumentation).

| Mode | Signal in the Event Log | MAST category |
|---|---|---|
| **step_repetition** | the same logical ticket merged more than once | System design (FM-1.3, the largest individual mode) |
| **premature_termination** | a ticket failed without delivering and without a dead dependency | Task verification |
| **dead_dependency_stall** | a ticket blocked or failed because a dependency can never complete | Inter-agent misalignment |
| **retry_churn** | proposals rejected by the Gate and re-attempted | Task verification |
| **worker_stall** | the Stall Detector flagged a worker with no progress | System design |
| **missing_verification** | a merge without a preceding `VerificationPassed` (must be **0**, enforced by the `MergeImpliesVerified` invariant) | Task verification |

Inspect it with `aoa diagnose [--json]`; the `aoa bench` table reports each strategy's total as a
`MAST` column, so the hermetic suite demonstrates **0 failure modes** alongside its other guarantees.
On live-LLM runs (see [`roadmap.md`](roadmap.md)) this histogram is the primary instrument for
checking whether the design's failure-mode *prevention* survives contact with a real agent.

## The verification blind spot (regression-escape rate)

The whole design bets that **verification, not intelligence, is the scaling constraint** — so the
system is only ever as good as its Gate. The `MergeImpliesVerified` invariant proves the Gate *ran*;
it says nothing about whether the Gate was *sufficient*. An agent can make the Gate green while
silently breaking something the Gate does not cover (on SWE-bench Lite, that is the `PASS_TO_PASS`
regression set; the merge queue also cannot catch two textually-disjoint changes that are *semantically*
incompatible — e.g. a signature change in one file and an old-style caller in another).

To make that ceiling **measured rather than assumed**, the Merge Queue takes an optional **Shadow**
verifier — a broader test set run against post-merge `main` *after* a proposal passes the Gate. It never
blocks or rolls back a merge (the Gate is the merge contract); a failure emits a `RegressionEscaped`
event, and `metrics` reports:

| Metric | What it measures | Why it matters |
|---|---|---|
| **regression_escape_rate** | `RegressionEscaped` / merges — merges the Gate accepted but the broader Shadow set rejected | The honest answer to "what new failure modes does a verification-centric architecture create"; a rising rate means the Gate is too narrow for the work. |

Configure it with `regression_verify` in `aoa.toml` (or `regression` on an `aoa eval` task). It is **off
by default** (empty ⇒ no shadow run). Where the Gate is the merge *contract*, the Shadow set is the
*audit* — the gap between them is exactly the blind spot, now a number.

## What we explicitly do NOT measure

Pheromone convergence, trust-score discrimination, and stability-horizon round counts — these belonged to
the market/consensus design that was rejected (see `docs/design/adr/005-no-markets-no-consensus.md`).
Correctness comes from an objective Gate, not from agent voting or reputation.
