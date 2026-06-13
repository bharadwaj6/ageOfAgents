# AGENTS.md

Guidance for AI agents and human contributors working in this repository. (Tool-agnostic; Claude Code
reads `CLAUDE.md`, which points here.)

## What this is

Age of Agents (`aoa`) is a **minimal, verifier-gated orchestrator** for fleets of AI coding agents. Read
[`README.md`](README.md) for the overview and [`docs/v2/architecture.md`](docs/v2/architecture.md) for the
design. The decisions that shape the codebase are recorded as ADRs in [`docs/v2/adr/`](docs/v2/adr/) —
**read them before making structural changes**, and add a new ADR when you make one.

## Golden rules (the design is opinionated on purpose)

1. **The event log is the single source of truth.** All state is a pure fold of `pkg/api` events
   (`internal/state`). Never introduce a separate mutable store. New behavior = new event type +
   fold case, not a side table. (ADR 001)
2. **Nothing merges that fails the objective verifier.** Correctness comes from `go build`/tests/lint,
   not from agent opinion. Do not add multi-agent voting/debate or consensus quorums. (ADR 002, 005)
3. **Coordination is deterministic Go; only work is done by LLMs.** There is exactly one reconciler
   (`internal/orchestrator`). Do not add role hierarchies, a second controller loop, or an LLM
   coordinator. (ADR 003)
4. **All LLM access goes through `agent.Backend`.** Never call a provider SDK/CLI from business logic.
   (ADR 004)
5. **Agents coordinate through shared state, not messages.** Extend the task graph by emitting
   `TicketCreated`; do not add agent-to-agent messaging or pheromone simulation. (ADR 006)
6. **Keep it small and portable.** One static binary, one config file, git only. Adding a third-party
   dependency or a required external service needs a strong justification (and probably an ADR).

## Build, test, run

```bash
go build ./... && go vet ./... && go test ./...   # must be green before any commit
gofmt -l cmd internal pkg                          # must print nothing

# Manual end-to-end (offline mock backend):
go build -o aoa ./cmd/aoa
./aoa init --path /tmp/town --repo ./demo
./aoa goal --path /tmp/town "Add a greeting function"
./aoa run  --path /tmp/town
./aoa status --path /tmp/town
```

The `mock` backend (`internal/agent/mock.go`) makes the **entire loop hermetic and offline** — the test
suite never makes network calls. Always keep it that way: tests must pass with no API keys.

## Repository map

| Path | Responsibility | Notes when changing |
|------|----------------|---------------------|
| `pkg/api` | Event envelope + typed payloads | Adding an event → add the payload type and a `state` fold case |
| `internal/ledger` | Append-only JSONL log | Keep `Append` concurrency-safe |
| `internal/state` | Pure fold → state, DAG readiness | Pure functions only; no I/O |
| `internal/orchestrator` | The single reconciler | Keep dispatch decoupled from the merge queue |
| `internal/agent` | `Backend` interface + `mock`/`claudecode` | New backends implement `Backend`; keep `mock` deterministic |
| `internal/worktree` | Git repo + isolated worktrees | All git calls use the config-independent identity helper |
| `internal/verify` | Objective verification gate | Pure command runner; no orchestration logic |
| `internal/mergequeue` | Verify → merge → rollback | Must leave `main` green and linearizable |
| `internal/config` | `aoa.toml` loading | Add a field → set a default in `Default()` |
| `cmd/aoa` | Tiny stdlib CLI | No CLI framework dependency |

## Conventions

- **Go style:** standard `gofmt`; doc comment on every exported symbol and package; table-driven tests.
- **TDD:** every change ships with a test. Mock external processes; use `t.TempDir()` for I/O. Tests must
  run offline.
- **Errors:** wrap with context (`fmt.Errorf("...: %w", err)`); surface errors, don't swallow them.
- **Commits:** Conventional Commits, subject ≤72 chars. Do **not** put AI model names in commit
  subjects/bodies (the `Co-Authored-By` trailer is the only AI attribution).
- **Worktree/branch:** this branch (`fresh-start`) lives in a git worktree; the town root stays on `main`.

## Common tasks

- **Add an agent backend:** implement `agent.Backend` (`Name`, `Run`), register it in
  `cmd/aoa/main.go:buildBackend`, document the `backend` value in `README.md`/`aoa.toml`.
- **Add a lifecycle event:** add the constant + payload in `pkg/api/events.go`, handle it in
  `internal/state/state.go:Apply`, emit it from `internal/orchestrator`, and cover it with a fold test.
- **Change the gate:** edit `verify` in `aoa.toml`; the queue verifies the post-merge state.
