# Age of Agents

A minimal, **verifier-gated** orchestrator for fleets of AI coding agents.

It rejects the org-chart metaphor (no "Mayor", no role hierarchy) and keeps only the small set of
distributed-systems primitives that map to *measured* multi-agent failure modes: an event-sourced
ledger, idempotent work tickets, a flat orchestrator–worker loop, and a serializing merge queue gated
by an **objective verifier** (your build/tests/lint). One static binary, one config file, git only —
no databases, brokers, or required external services.

> The bottleneck in multi-agent coding is **verification + specification + idempotency**, not hierarchy,
> markets, or multi-agent debate. So we invest there and nowhere else. See
> [`docs/v2/architecture.md`](docs/v2/architecture.md) and the [ADRs](docs/v2/adr/).

## How it works

```
goal ─▶ reconciler (deterministic) ─▶ workers in isolated git worktrees ─▶ merge queue (verify ▶ merge)
        observe(ledger)→fold→act         run the agent backend                 only green code lands on main
```

The orchestrator decomposes a goal into tickets, dispatches dependency-ready ones to agents under a
concurrency governor, and serializes merges into `main` behind the verifier. Everything is an append-only
event; all state is a pure fold of the log (replayable, auditable). Agents coordinate through the shared
log (a blackboard), never by messaging each other.

## Quick start

```bash
go build -o aoa ./cmd/aoa

# Scaffold a town with an integration repo (a minimal Go module)
./aoa init --path ./town --repo ./demo

# Submit a goal and run the loop (offline mock backend by default)
./aoa goal --path ./town "Add a greeting function"
./aoa run  --path ./town

# Inspect
./aoa status --path ./town       # goals + ticket states
./aoa events --path ./town tail  # the audit trail
```

With the default **mock** backend the entire loop runs offline (no API calls), which is exactly how the
test suite exercises it. Switch `backend = "claudecode"` in `aoa.toml` to drive a real coding agent.

## Configuration (`aoa.toml`)

```toml
repo             = "./demo"          # integration git repository
backend          = "mock"            # "mock" (offline) | "claudecode" (real agent)
concurrency      = 4                 # max workers in flight (the governor)
max_attempts     = 2                 # retries before a ticket fails
conventions_file = "CONVENTIONS.md"  # injected into every agent prompt
verify = [                           # the objective gate; nothing merges unless this passes
  ["go", "build", "./..."],
  ["go", "test", "./..."],
]
```

## Commands

| Command | Purpose |
|---------|---------|
| `aoa init` | Scaffold a town + integration repo and `aoa.toml` |
| `aoa goal "…"` | Submit a goal |
| `aoa run [--once]` | Run the reconciler (loops to completion; `--once` for a single pass) |
| `aoa status` | Show goals and ticket states |
| `aoa feed [--type T]` | Print the event stream |
| `aoa events tail [--count N] \| replay` | Inspect the log |

## Development

```bash
go build ./... && go vet ./... && go test ./...
```

The `mock` backend makes the full orchestration loop hermetic and offline in tests. Real agent calls are
isolated behind the [`agent.Backend`](internal/agent/agent.go) interface.

## Layout

| Path | Responsibility |
|------|----------------|
| `pkg/api` | Event envelope and typed payloads |
| `internal/ledger` | Append-only JSONL event log |
| `internal/state` | Pure fold of events → state, DAG readiness |
| `internal/orchestrator` | The single reconciler |
| `internal/agent` | `Backend` interface + `mock`/`claudecode` |
| `internal/worktree` | Git repo + isolated worktrees |
| `internal/verify` | Objective verification gate |
| `internal/mergequeue` | Verify-then-merge into `main` |
| `internal/config` | `aoa.toml` loading |
| `cmd/aoa` | CLI |

## Documentation

- [`docs/v2/architecture.md`](docs/v2/architecture.md) — the design, with the research it rests on
- [`docs/v2/adr/`](docs/v2/adr/) — the load-bearing decisions
- `docs/*.md` — the research corpus (`claude.md`, `gemini.md`, `grok.md`, `perplexity.md`, `research_links.md`)
