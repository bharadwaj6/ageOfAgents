# Age of Agents

**Age of Agents** (`aoa`) is **not a multi-agent framework. It's a deterministic build system whose
compile step happens to be a stochastic LLM** — Bors for AI agents.

Scheduling, state, merge, and done-ness are plain, deterministic Go gated on objective signals (your
build, your tests, the compiler). The LLM only ever emits a *candidate diff*; whether it lands is decided
by your Gate, not by the agent. That inverts the usual bet: where most agent frameworks chase orchestration
cleverness — which better models erode — `aoa`'s bet is that **verification, not intelligence, is the
scaling constraint**. Better models only sharpen the worker; the control plane is unchanged.

You give it a **Goal**. It breaks that into **Tasks**, dispatches each to a **Worker** (an agent in an
isolated git worktree — i.e. a worker process that emits a candidate diff), and merges results into `main`
**only if your build and tests pass**. One binary, one config file, git only — no database, no broker, no
LLM coordinator.

## Why this is different — the receipts

This isn't a gentler org chart (no Mayor/Witness/Deacon, no role hierarchy, no agents chatting). It's the
boring distributed-systems core, with proofs attached:

- **The Event Log is the single source of truth** — an append-only JSONL ledger; all state is derived by
  replay, so crash recovery, audit, and every metric come for free (ADR 001).
- **A verifier-gated, serializing merge queue** keeps `main` linearizable and always green: merge → run
  the Gate on the *post-merge* state → keep it only if it passes, else roll back (ADR 002).
- **One deterministic Scheduler**, not eleven — no LLM in the control plane; coordination is plain Go via
  the shared log (ADR 003).
- **Proven, not asserted:** a hermetic Jepsen-style invariant harness with seeded fault injection, plus a
  TLA+ model of the merge/approval invariants. The whole test suite is offline (the `mock` Backend never
  touches the network).
- **Honest about its own ceiling:** because the system is only as good as its Gate, it *measures* the
  blind spot — a [regression-escape rate](docs/design/metrics.md) (merges the Gate accepted but a broader
  shadow test set would reject) and a per-run **MAST** failure-mode histogram (`aoa diagnose`).
- **Cost-aware and bounded:** token/`$` accounting per ticket and per goal, a per-goal **spend governor**
  (circuit breaker), and retry **backoff + crash-loop** detection so a runaway loop can't burn your budget.
- **OpenTelemetry-native, vendor-agnostic:** every run replays into OTLP **traces** (goal → ticket →
  attempt spans) and **metrics** — point it at Honeycomb, Grafana Tempo, Datadog, or any OTLP backend via
  the standard env vars (`aoa run --otel`). Off by default; observability is just another replay
  projection of the log, not hot-path instrumentation (ADR 012). See [`docs/integrations/`](docs/integrations/README.md).
- **Adoptable and recoverable:** point it at your own repo on any branch (`aoa init --adopt`); on a
  terminal failure it preserves the agent's worktree and hands it back to you (`aoa status`).

> **SWE-bench Lite:** no headline solve-rate, and the reason is worth stating. Every run recorded in
> `logs/run_evaluation/` (best: 10/11 with `grok`) was produced with **aoa's Gate disabled** — the eval
> script passed `--inference-mode`, so every proposal merged unconditionally. Those numbers were scored
> honestly by the official Docker harness, but they measure the backend agent, not the verifier-gated
> merge queue this project is about.
>
> The first Gate-on vs Gate-off comparison ran on 2026-08-23 (see
> [`docs/design/live_eval.md`](docs/design/live_eval.md)): on 2 instances, both configurations resolved
> both, and the Gate rejected 1 of 2 proposals on `astropy-14365` — catching a patch that broke the
> repo's existing tests and recovering on retry. That is the mechanism working, at a cost of one extra
> attempt. It is not yet evidence that the Gate changes outcomes; two instances support no rate.

## What it aims to be

The thesis: **verification, not intelligence, is the scaling constraint for agentic coding.** Most agent
frameworks chase orchestration cleverness (role hierarchies, debate, consensus) — exactly the part that
better models erode. `aoa` bets the opposite way: keep scheduling, state, merge, and done-ness as plain
deterministic Go gated on objective signals (your build, your tests, the compiler), and let the LLM only
ever emit a *candidate diff* that the Gate, not the agent, decides on. Better models then only sharpen the
worker; the control plane is unchanged.

The goal is to be a **tool engineers use daily on real repositories** — not a demo. That means: correct
by construction (done), cost-bounded and observable so you can run it on real money and see what happened
(done), adoptable into an existing project in minutes (done), and **empirically validated at scale** —
which is the part still outstanding. The eval harness runs end-to-end against SWE-bench Lite through the
official Docker scorer, but no run so far has had the Gate switched on, so the central claim is untested.
Closing that is the next milestone, not a finished one.


## Roadmap

We've achieved the `v0.1` milestone, bringing dynamic DAG re-evaluation, multi-model fallbacks, log compaction, Docker sandboxing, and native GitHub Actions CI integration.

Looking forward, the high-level roadmap and long-term items include:

1. **Firecracker MicroVM Sandboxing:** We currently support Docker for sandboxing the `Verifier` gate. A future implementation will support Firecracker for stronger multi-tenant isolation and lower overhead, running the verification and agent execution in secure microVMs.
2. **Persistent Server Mode Enhancements:** We have an initial `aoa serve` implementation for GitHub webhooks. This will be expanded into a robust persistent server mode with a dashboard UI to inspect the Event Log, view active task graphs, and manage the `aoa` orchestrator remotely.
3. **A `$` Governor in the Control Plane:** The orchestrator currently has a *token* spend governor and a circuit breaker for eval loops. A true cross-run *dollar* circuit breaker is a planned follow-up.
4. **Cross-Repo Dependency Management:** Currently, `aoa` handles tasks within a single repository workspace. Future enhancements will allow the orchestrator to manage goals spanning multiple repositories, orchestrating atomic merges across microservices.
5. **Deferred Research Bets:** Speculative/batched merge with an adaptive window, best-of-N with the test suite as verifier, and SPRT early-stopping for live evals. These will be A/B tested against our SWE-bench baseline to measure their impact.

## Core Concepts

| Concept | What it is |
|---|---|
| **Goal** | What you want done, in plain English |
| **Task** | A single piece of work derived from a Goal |
| **Worker** | An AI agent working on one Task in an isolated git worktree |
| **Event Log** | An append-only file that records everything that happens — the single source of truth |
| **Scheduler** | The deterministic loop that reads the Event Log, finds ready work, and dispatches Workers |
| **Gate** | Your build + test commands (e.g. `go build`, `go test`) that every change must pass |
| **Merge Queue** | Runs the Gate on each Worker's output; only passing code reaches `main` |
| **Backend** | The AI engine Workers use — `mock` for offline testing, `claudecode` for real work |

## How It Works

```
1. You submit a Goal        →  "Add a greeting function"
2. The Scheduler creates    →  Tasks (with dependency ordering)
3. Workers execute          →  Each in its own isolated git worktree
4. The Gate verifies        →  Your build + tests must pass
5. The Merge Queue lands    →  Only verified code reaches main
```

Everything is recorded in the Event Log. State is rebuilt by replaying it — crash recovery, audit trails, and debugging come for free.

## Quick Start

### 1. Install the CLI

Download the latest binary for your OS from the [GitHub Releases](https://github.com/bharadwaj6/ageOfAgents/releases) page, or build it yourself:

```bash
git clone https://github.com/bharadwaj6/ageOfAgents.git
cd ageOfAgents
go build -o aoa ./cmd/aoa
```

### 2. Create a workspace

```bash
./aoa init --path ./workspace --repo ./demo      # scaffold a demo repo to try it out
# — or — adopt your own repo (on whatever branch it's on), Gate auto-detected:
./aoa init --path . --adopt /path/to/my-repo
```

`--repo` scaffolds a throwaway demo; `--adopt` points `aoa` at an existing repository as-is (no files
written into your tree) and sniffs a starting Gate from the project (`go.mod` → `go build`/`go test`,
`package.json` → `npm test`, Python → `pytest`, `Makefile` → `make test`). Either way you get an
`aoa.toml` config and an Event Log under `.aoa/`.

### 3. Submit a Goal

```bash
./aoa goal --path ./workspace "Add a greeting function"
```

### 4. Run the Scheduler

```bash
./aoa run --path ./workspace
```

By default, the `mock` Backend runs everything offline — no API keys, no cost. Great for trying things out.

### 5. See what happened

```bash
./aoa status --path ./workspace       # Goals + Task states
./aoa events --path ./workspace tail  # The Event Log
```

### Using a real AI agent

Edit `aoa.toml` in your workspace:

```toml
backend = "openai"
```

Set the `OPENAI_API_KEY` environment variable, then run `./aoa run` again. The Scheduler will dispatch Tasks to a real coding agent natively without requiring external CLIs. You can also use `backend = "anthropic"` (set `ANTHROPIC_API_KEY`, no CLI needed), `"claudecode"`, or `"grok"`.
## Configuration (`aoa.toml`)

```toml
repo                = "./demo"          # git repository for agents to work on
backend             = "mock"            # "mock" (offline) | "openai" | "anthropic" | "claudecode" | "grok"
concurrency         = 4                 # max Workers running at once
max_attempts        = 2                 # retries before a Task fails
best_of_n           = 1                 # concurrent attempts per Task; the Gate picks the winner
conventions_file    = "CONVENTIONS.md"  # coding rules injected into every agent prompt
require_approval     = false            # if true, park each verified proposal for `aoa approve`
max_tokens_per_goal = 0                 # spend governor: per-goal token ceiling (0 = unlimited)
max_usd_per_goal    = 0                 # spend governor: per-goal $ ceiling (needs [pricing])
retry_backoff       = "0s"              # wait before re-dispatching a failed Task (grows per attempt)
crash_loop_threshold = 3                # give up after N identical failures, even under max_attempts
stall_timeout       = "2m"              # no-progress timeout before a Worker is restarted
agent_timeout       = "30m"             # hard ceiling on one agent attempt (bounds runtime, not silence)
max_passes          = 1000              # hard cap on reconcile passes in one run
max_graph_depth     = 5                 # graph governor: emergent decomposition depth
max_tickets_per_goal = 64               # graph governor: total Tasks one Goal may spawn
max_fan_out         = 8                 # graph governor: children per decomposition
sandbox             = ""                # "" (host) | "docker" — how the Gate's commands are isolated
sandbox_image       = ""                # image for sandbox="docker" (default golang:1.22; set for non-Go gates)
verify = [                              # the Gate — nothing merges unless this passes
  ["go", "build", "./..."],
  ["go", "test", "./..."],
]
regression_verify = []                  # optional broader set; measures the regression-escape rate
                                        # (never blocks a merge — see docs/design/metrics.md)

[pricing]                               # optional: $ per *million* tokens, by model — powers cost columns
# claudecode = 15.0
```

Every field, with defaults and when to set it, is in the [configuration reference](docs/config-reference.md);
a worked config and copy-paste runbook are in [`examples/`](examples/).

## Commands

| Command | What it does |
|---------|--------------|
| `aoa init [--repo \| --adopt PATH]` | Scaffold a demo, or adopt your own repo (Gate auto-detected) |
| `aoa goal "…"` | Submit a Goal |
| `aoa run [--once\|--interval D] [--otel\|--otel-live]` | Run the Scheduler (reconciles to settled, then exits `0`; `--once` for a single pass, `--interval` to keep going on a cadence). Safe to re-run any time — see [scheduling](docs/scheduling.md) |
| `aoa status [--watch]` | Goals, Task states, per-ticket tokens, run cost, and a "needs human" handoff for failures |
| `aoa amend <goal> "…"` | Append steering guidance to a Goal mid-run (future dispatches pick it up; ADR — `GoalAmended`) |
| `aoa approve \| reject <ticket>` | Decide a proposal parked by the approval gate (ADR 008) |
| `aoa events tail [--count N] \| replay` (`aoa feed` = deprecated alias) | Inspect the Event Log |
| `aoa diagnose [--json]` | MAST-style failure-mode histogram for a run |
| `aoa eval --tasks T [--price-file F] [--max-cost $] [--otel]` | Run end-to-end eval tasks; per-task success, tokens, `$` (with a cost ceiling), MAST |
| `aoa bench [--json]` | The hermetic coordination benchmark |
| `aoa otel export` · `aoa run --otel[-live]` | Replay the Event Log to OTLP traces + metrics — post-hoc, or `--otel-live` to stream live (any OpenTelemetry backend) |

## Project Layout

| Path | What it does |
|------|--------------|
| `pkg/api` | Event types and typed payloads |
| `internal/ledger` | Append-only JSONL Event Log |
| `internal/state` | Replays events into current state (Task readiness, dependencies) |
| `internal/orchestrator` | The Scheduler — the single control loop |
| `internal/agent` | Backend interface + `mock` / `claudecode` / `grok` implementations |
| `internal/worktree` | Git worktree management for isolated Worker sandboxes |
| `internal/verify` | The Gate — runs your verification commands |
| `internal/mergequeue` | The Merge Queue — verify then merge into `main` |
| `internal/metrics` · `internal/diagnose` | Replay projections: run metrics + the MAST failure-mode histogram |
| `internal/otel` | Replay projection to OpenTelemetry (OTLP traces + metrics), off by default (ADR 012) |
| `internal/bench` · `internal/liveeval` | Hermetic coordination benchmark + end-to-end live eval harness |
| `internal/config` | `aoa.toml` loading |
| `cmd/aoa` | CLI entry point |
| `scripts/` | Helper scripts for sandboxing (gVisor injection) |

## Development

A `Makefile` wraps the common commands (`make help` lists them all):

```bash
make check   # pre-commit gate: build + vet + test + gofmt
make build   # build the ./aoa binary
make test    # full hermetic suite  (make test-short = faster)
make bench   # the coordination benchmark
```

Opt-in targets need external tools: `make smoke` (live run, needs an authenticated `claude` CLI) and
`make formal` (TLA+ check, needs `java` + `TLA2TOOLS=path/to/tla2tools.jar`).

The `mock` Backend makes the full loop hermetic and offline in tests. Real agent calls are isolated behind the [`Backend`](internal/agent/agent.go) interface.

## Documentation

- [`docs/design/architecture.md`](docs/design/architecture.md) — design and research basis
- [`docs/design/cross_repo.md`](docs/design/cross_repo.md) — architecture for atomic multi-repo merges
- [`docs/config-reference.md`](docs/config-reference.md) — every `aoa.toml` field, defaults, when to set it
- [`docs/scheduling.md`](docs/scheduling.md) — running `aoa` on a cadence (cron, systemd, Actions, webhooks)
- [`docs/design/loop_engineering.md`](docs/design/loop_engineering.md) — how `aoa` scores against the loop-engineering model, and what it refuses
- [`docs/integrations/`](docs/integrations/README.md) — OpenTelemetry/OTLP (Honeycomb, etc.) + agent backends
- [`examples/`](examples/) — a copy-paste runbook for adopting your own repo
- [`docs/design/getting_started.md`](docs/design/getting_started.md) — step-by-step tutorial
- [`docs/design/live_eval.md`](docs/design/live_eval.md) — running aoa with a real agent (smoke test + SWE-bench)
- [`docs/design/adr/`](docs/design/adr/) — architecture decision records
- [`docs/design/metrics.md`](docs/design/metrics.md) — success metrics
- `docs/*.md` — the research corpus
