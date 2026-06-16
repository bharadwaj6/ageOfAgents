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

> **SWE-bench Lite:** the headline solve-rate / cost-per-solve number goes here once the at-scale run lands
> (the harness is ready; see [`docs/design/live_eval.md`](docs/design/live_eval.md)). Until then, every
> number in this repo comes from the hermetic `mock` backend and is labeled as such.

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

### 1. Build the CLI

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
backend = "claudecode"
```

Then run `./aoa run` again. The Scheduler will dispatch Tasks to a real coding agent.

## Configuration (`aoa.toml`)

```toml
repo                = "./demo"          # git repository for agents to work on
backend             = "mock"            # "mock" (offline) | "claudecode" | "grok"
concurrency         = 4                 # max Workers running at once
max_attempts        = 2                 # retries before a Task fails
conventions_file    = "CONVENTIONS.md"  # coding rules injected into every agent prompt
require_approval     = false            # if true, park each verified proposal for `aoa approve`
max_tokens_per_goal = 0                 # spend governor: per-goal token ceiling (0 = unlimited)
retry_backoff       = "0s"              # wait before re-dispatching a failed Task (grows per attempt)
crash_loop_threshold = 3                # give up after N identical failures, even under max_attempts
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
| `aoa run [--once]` | Run the Scheduler (loops to completion; `--once` for a single pass) |
| `aoa status` | Goals, Task states, per-ticket tokens, run cost, and a "needs human" handoff for failures |
| `aoa approve \| reject <ticket>` | Decide a proposal parked by the approval gate (ADR 008) |
| `aoa feed [--type T]` | Print the event stream |
| `aoa events tail [--count N] \| replay` | Inspect the Event Log |
| `aoa diagnose [--json]` | MAST-style failure-mode histogram for a run |
| `aoa eval --tasks T [--price-file F] [--max-cost $] [--otel]` | Run end-to-end eval tasks; per-task success, tokens, `$` (with a cost ceiling), MAST |
| `aoa bench [--json]` | The hermetic coordination benchmark |
| `aoa otel export` · `aoa run --otel` | Replay the Event Log to OTLP traces + metrics (any OpenTelemetry backend) |

## Project Layout

| Path | What it does |
|------|--------------|
| `pkg/api` | Event types and typed payloads |
| `internal/ledger` | Append-only JSONL Event Log |
| `internal/state` | Replays events into current state (Task readiness, dependencies) |
| `internal/orchestrator` | The Scheduler — the single control loop |
| `internal/agent` | Backend interface + `mock` / `claudecode` implementations |
| `internal/worktree` | Git worktree management for isolated Worker sandboxes |
| `internal/verify` | The Gate — runs your verification commands |
| `internal/mergequeue` | The Merge Queue — verify then merge into `main` |
| `internal/config` | `aoa.toml` loading |
| `cmd/aoa` | CLI entry point |

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
- [`docs/design/getting_started.md`](docs/design/getting_started.md) — step-by-step tutorial
- [`docs/design/live_eval.md`](docs/design/live_eval.md) — running aoa with a real agent (smoke test + SWE-bench)
- [`docs/design/adr/`](docs/design/adr/) — architecture decision records
- [`docs/design/metrics.md`](docs/design/metrics.md) — success metrics
- `docs/*.md` — the research corpus
