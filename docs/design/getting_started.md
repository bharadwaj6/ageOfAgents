# Getting Started with Age of Agents

This guide walks you through your first run of **Age of Agents** (`aoa`) — from zero to a fully orchestrated, verified code change in under five minutes.

## Prerequisites

- **Go 1.21+** — [install Go](https://go.dev/doc/install)
- **git** — any recent version

## Step 1: Build the CLI

Clone the repo and compile the `aoa` binary:

```bash
git clone https://github.com/bharadwaj6/ageOfAgents.git
cd ageOfAgents
go build -o aoa ./cmd/aoa
```

You now have a single `aoa` binary. No other dependencies needed.

## Step 2: Create a Workspace

A **workspace** is a directory where `aoa` keeps everything: your project's git repo, the Event Log, configuration, and worker sandboxes.

```bash
./aoa init --path ./workspace --repo ./demo
```

**What just happened?**
- `./workspace/.aoa/` — the Event Log and worktree storage
- `./workspace/aoa.toml` — configuration file (Backend, Gate commands, concurrency)
- `./workspace/CONVENTIONS.md` — coding rules injected into every agent prompt
- `./workspace/demo/` — a minimal Go module (your target repo)

### Or: adopt your own repo

To point `aoa` at an **existing** repository instead of a scaffolded demo, use
`--adopt`. It works on whatever branch the repo is currently checked out on
(`main`, `master`, a feature branch — `aoa` cuts worker branches from `HEAD`),
writes nothing into your tree, and auto-detects a sensible Gate:

```bash
./aoa init --path . --adopt /path/to/my-repo
```

`aoa` sniffs the project and writes a starting `verify` Gate into `aoa.toml` —
`go build`/`go test` for a `go.mod`, `npm test` for a `package.json`,
`python -m pytest` for a Python project, or `make test` for a `Makefile`. Review
and adjust it (provisioning the test environment is your job — see
[ADR 009](adr/009-live-evaluation-out-of-hermetic-suite.md)). `init` never
overwrites an existing `aoa.toml`; pass `--force` to replace one.

## Step 3: Submit a Goal

A **Goal** is what you want done, in plain English. The Scheduler will break it into Tasks automatically.

```bash
./aoa goal --path ./workspace "Add a greeting function to the main package"
```

Check the current state:

```bash
./aoa status --path ./workspace
```

You should see your Goal listed with a pending Task.

## Step 4: Run the Scheduler

The **Scheduler** reads the Event Log, finds ready Tasks, dispatches Workers (AI agents) to execute them, and merges verified results into `main`.

```bash
./aoa run --path ./workspace
```

**What just happened?**
1. The Scheduler created a Task from your Goal
2. A Worker claimed the Task and worked in an isolated git worktree
3. The **Gate** ran your verification commands (`go build`, `go test`)
4. The **Merge Queue** merged the passing change into `main`

By default, `aoa.toml` uses the `mock` Backend — a deterministic, offline simulator. No API keys or network access needed. This is exactly how the test suite works too.

## Step 5: Inspect the Results

See what happened:

```bash
# Goal and Task states
./aoa status --path ./workspace

# Event-by-event audit trail
./aoa events --path ./workspace tail

# Full event stream (filterable by type)
./aoa feed --path ./workspace
```

Every action is recorded in the Event Log. You can replay it to reconstruct any past state.

## Step 6: Use a Real AI Agent

Ready to run a real coding agent? Edit `aoa.toml`:

```toml
backend = "claudecode"
```

Then submit a new goal and run again:

```bash
./aoa goal --path ./workspace "Add unit tests for the greeting function"
./aoa run  --path ./workspace
```

The Scheduler will invoke the `claudecode` Backend, which drives a real coding agent as a subprocess in the Task's isolated worktree. The agent's code changes are only merged if they pass your Gate.

## Step 7: Customize the Gate

The **Gate** is the set of commands that every code change must pass before it can merge. Customize it in `aoa.toml`:

```toml
verify = [
  ["go", "build", "./..."],
  ["go", "test", "./..."],
  ["golangci-lint", "run"]
]
```

This is how `aoa` guarantees that `main` is always green — no change lands without passing your build, tests, and linter.

## Concepts Recap

| Concept | What it is |
|---|---|
| **Goal** | What you want done |
| **Task** | A piece of work, auto-created from a Goal |
| **Worker** | An AI agent executing one Task in isolation |
| **Event Log** | Append-only record of everything — the single source of truth |
| **Scheduler** | Deterministic loop: reads log → dispatches workers → drives merges |
| **Gate** | Your build/test commands that every change must pass |
| **Merge Queue** | Serializes merges to `main` — only Gate-passing code lands |
| **Backend** | The AI engine (`mock` for testing, `claudecode` for real work) |

## Next Steps

- **[Architecture](architecture.md)** — how the Scheduler, Event Log, and Merge Queue fit together
- **[Architecture Decision Records](adr/)** — the design decisions and the research behind them
- **[Success Metrics](metrics.md)** — how we measure whether the design works
