# AGENTS.md

Guidance for AI agents and human contributors. (Claude Code reads `CLAUDE.md`, which points here.)

Age of Agents (`aoa`) is a minimal, Gate-verified orchestrator for fleets of AI coding agents: every agent
works in a throwaway git worktree, and a merge queue merges only what passes the project's build and tests.

## Vocabulary: docs vs. code

| Docs say | Code says | Where |
|---|---|---|
| Task | `Ticket` | `internal/state`, `pkg/api` |
| Scheduler | `Orchestrator`, `ReconcileOnce` | `internal/orchestrator` |
| Event Log | `Ledger` | `internal/ledger` |
| Gate | `Verifier`, `verify.Command` | `internal/verify` |
| Merge Queue | `mergequeue.Queue` | `internal/mergequeue` |
| Replay | `state.Fold` | `internal/state` |
| Backend | `agent.Backend` | `internal/agent` |

## Golden rules

The design is opinionated on purpose. Contradicting one of these needs a new ADR, not a quiet edit.

1. **The Event Log is the single source of truth.** All state is a replay of `pkg/api` events; new
   behaviour is a new event type plus a `state.Apply` case, never a side table. (ADR 001)
2. **Nothing merges that fails the Gate.** Correctness comes from the build, tests and linter, not from
   agent opinion — no voting, debate or consensus. (ADR 002, 005)
3. **Coordination is deterministic Go; only the work is done by LLMs.** Exactly one Scheduler; no role
   hierarchy, no second control loop, no LLM coordinator. (ADR 003)
4. **All LLM access goes through `agent.Backend`.** Never a provider SDK or CLI from business logic. (ADR 004)
5. **Agents coordinate through the shared log, not messages.** Extend the graph by emitting
   `TicketCreated`. (ADR 006)
6. **Keep it small and portable.** One static binary, one config file, git only. A new dependency or a
   required external service needs strong justification. The OpenTelemetry SDK is the one sanctioned
   cluster, isolated in `internal/otel` and opt-in. (ADR 012)

## Build and test

Run `make check` (build + vet + test + gofmt) before every commit and **read its output** — do not pipe it
through `tail` and trust the exit code, which reports the pipeline's last command. `make help` lists every
other target. The suite is hermetic: the `mock` backend never networks, and tests must pass with no API keys.

## Repository map

| Path | Responsibility | When changing |
|---|---|---|
| `pkg/api` | Event envelope + typed payloads | New event → payload type + `state.Apply` case |
| `internal/ledger` | Append-only JSONL Event Log | Keep `Append` concurrency-safe |
| `internal/state` | Replay → state, Task Graph readiness | Pure functions, no I/O |
| `internal/orchestrator` | The Scheduler (single control loop) | Keep dispatch decoupled from the Merge Queue |
| `internal/agent` | `Backend` interface, CLI presets, native `openai`/`anthropic`, `mock` | Keep `mock` deterministic |
| `internal/worktree` | Git repo + isolated worktrees | Git calls use the config-independent identity helper |
| `internal/verify` | The Gate | Pure command runner, no orchestration logic |
| `internal/mergequeue` | Verify → merge → rollback, disjoint-file batching | Must leave `main` green and linearizable |
| `internal/metrics`, `internal/diagnose` | Run metrics + MAST failure-mode histogram | Replay projections; no instrumentation |
| `internal/otel` | Replay projection to OTLP traces + metrics | Off by default, never in the hot path, never networks in tests |
| `internal/bench`, `internal/liveeval` | Hermetic benchmark + live eval harness | `liveeval` networks only with a networked Backend |
| `internal/config` | `aoa.toml` loading | New field → default in `Default()` |
| `cmd/aoa` | Tiny stdlib CLI | No CLI framework; document new commands in `docs/cli.md` |
| `scripts/` | Eval + benchmark harnesses, installer | Not covered by `make check`; keep runnable from a clean clone |

## Conventions

- `gofmt`; doc comment on every exported symbol and package; table-driven tests; `t.TempDir()` for I/O.
- Every change ships a test, and the test fails without the fix — verify that.
- Handle every error; never assign one to `_`. Wrap with `fmt.Errorf("context: %w", err)`.
- Accept interfaces, return concrete types. Interfaces end in `-er`. No stuttering (`user.ID`).
- `main` is protected: work in a `git worktree` off `main`, one PR per increment, rebase-merge.
- Conventional Commits, subject ≤72 chars. No AI model names in a subject or body.

## Task recipes

Step-by-step for specific jobs lives in `.claude/skills/` — read the matching `SKILL.md` before starting.

| Skill | Read it when |
|---|---|
| `change-architecture` | making a structural change, adding a dependency, or asking whether `aoa` should do X |
| `add-agent-backend` | adding or changing an agent backend or CLI harness |
| `add-event-or-metric` | adding a lifecycle event, state field, metric or span |
| `publish-docs-site` | editing `docs/`, adding a page, or fixing an MkDocs build |
| `delegating-work-to-aoa` | driving the `aoa` CLI itself to get a change gated |
