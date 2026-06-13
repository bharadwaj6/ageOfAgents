# Age of Agents

**Age of Agents** (`aoa`) coordinates a fleet of AI coding agents working on your codebase — safely and deterministically, with near-zero coordination overhead and nothing merged unless your build and tests pass.

You give it a **Goal** (what you want built). It breaks that Goal into **Tasks**, dispatches each Task to a **Worker** (an AI agent in an isolated git checkout), and merges the results into `main` — but only if your build and tests pass. One binary, one config file, git only.

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
./aoa init --path ./workspace --repo ./demo
```

This creates a workspace with an `aoa.toml` config, an Event Log, and a `demo/` git repo for your agents to work on.

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
repo             = "./demo"          # git repository for agents to work on
backend          = "mock"            # "mock" (offline) | "claudecode" (real agent)
concurrency      = 4                 # max Workers running at once
max_attempts     = 2                 # retries before a Task fails
conventions_file = "CONVENTIONS.md"  # coding rules injected into every agent prompt
verify = [                           # the Gate — nothing merges unless this passes
  ["go", "build", "./..."],
  ["go", "test", "./..."],
]
```

## Commands

| Command | What it does |
|---------|--------------|
| `aoa init` | Create a workspace + git repo and `aoa.toml` |
| `aoa goal "…"` | Submit a Goal |
| `aoa run [--once]` | Run the Scheduler (loops to completion; `--once` for a single pass) |
| `aoa status` | Show Goals and Task states |
| `aoa feed [--type T]` | Print the event stream |
| `aoa events tail [--count N] \| replay` | Inspect the Event Log |
| `aoa diagnose [--json]` | MAST-style failure-mode histogram for a run |

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

```bash
go build ./... && go vet ./... && go test ./...
```

The `mock` Backend makes the full loop hermetic and offline in tests. Real agent calls are isolated behind the [`Backend`](internal/agent/agent.go) interface.

## Documentation

- [`docs/design/architecture.md`](docs/design/architecture.md) — design and research basis
- [`docs/design/getting_started.md`](docs/design/getting_started.md) — step-by-step tutorial
- [`docs/design/adr/`](docs/design/adr/) — architecture decision records
- [`docs/design/metrics.md`](docs/design/metrics.md) — success metrics
- `docs/*.md` — the research corpus
