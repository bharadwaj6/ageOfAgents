# AGENTS.md

Guidance for AI agents and human contributors working in this repository. (Tool-agnostic; Claude Code
reads `CLAUDE.md`, which points here.)

## What this is

Age of Agents (`aoa`) is a **minimal, Gate-verified orchestrator** for fleets of AI coding agents. Read
[`README.md`](README.md) for the overview and [`docs/design/architecture.md`](docs/design/architecture.md) for the
design. The decisions that shape the codebase are recorded as ADRs in [`docs/design/adr/`](docs/design/adr/) —
**read them before making structural changes**, and add a new ADR when you make one.

## Glossary: docs vs. code

User-facing docs use plain names; the Go code uses more specific identifiers. Here's the mapping:

| Docs name | Go identifier | Where in code |
|-----------|---------------|---------------|
| Task | `Ticket`, `TicketCreated`, etc. | `internal/state`, `pkg/api` |
| Scheduler | `Orchestrator`, `ReconcileOnce` | `internal/orchestrator` |
| Event Log | `Ledger` | `internal/ledger` |
| Gate | `Verifier`, `verify.Command` | `internal/verify` |
| Merge Queue | `mergequeue.Queue` | `internal/mergequeue` |
| Replay (computing state) | `state.Fold` | `internal/state` |
| Backend | `agent.Backend` | `internal/agent` |
| Concurrency Limit | `Options.Concurrency` | `internal/orchestrator` |
| Stall Detector | `detectStalled` | `internal/orchestrator` |
| Conventions | `Options.Conventions` | `internal/orchestrator`, `internal/config` |

## Golden rules (the design is opinionated on purpose)

1. **The Event Log is the single source of truth.** All state is derived by replaying `pkg/api` events
   (`internal/state`). Never introduce a separate mutable store. New behavior = new event type +
   replay case, not a side table. (ADR 001)
2. **Nothing merges that fails the Gate.** Correctness comes from `go build`/tests/lint,
   not from agent opinion. Do not add multi-agent voting/debate or consensus quorums. (ADR 002, 005)
3. **Coordination is deterministic Go; only work is done by LLMs.** There is exactly one Scheduler
   (`internal/orchestrator`). Do not add role hierarchies, a second controller loop, or an LLM
   coordinator. (ADR 003)
4. **All LLM access goes through `agent.Backend`.** Never call a provider SDK/CLI from business logic.
   (ADR 004)
5. **Agents coordinate through the Shared Log, not messages.** Extend the Task Graph by emitting
   `TicketCreated`; do not add agent-to-agent messaging. (ADR 006)
6. **Keep it small and portable.** One static binary, one config file, git only. Adding a third-party
   dependency or a required external service needs a strong justification (and probably an ADR). The one
   sanctioned dependency cluster is the OpenTelemetry SDK (ADR 012) — and it stays **isolated in
   `internal/otel` and opt-in**: the binary still needs zero external services to run, and the hermetic
   suite never networks. New observability = extend the replay projection in `internal/otel`, never
   instrument the control loop.

## Build, test, run

A `Makefile` wraps the common commands — run `make help` for the full list. The everyday ones:

| Target | What it runs |
|--------|--------------|
| `make check` | the pre-commit gate: `build` + `vet` + `test` + `gofmt` check (run this before any commit) |
| `make build` | `go build -o aoa ./cmd/aoa` |
| `make test` / `make test-short` | full suite / faster (fewer chaos seeds) |
| `make bench` | the hermetic coordination benchmark (`aoa bench`) |
| `make chaos` | the fault-injection soak (`COUNT=N` for a longer run) |
| `make smoke` | live real-LLM smoke test (needs an authenticated `claude` CLI) |
| `make formal` | model-check the TLA+ spec (needs `java` + `TLA2TOOLS=path/to/tla2tools.jar`) |

The raw commands those wrap (use directly if you prefer):

```bash
go build ./... && go vet ./... && go test ./...   # must be green before any commit
gofmt -l cmd internal pkg                          # must print nothing

# Manual end-to-end (offline mock Backend):
go build -o aoa ./cmd/aoa
./aoa init --path /tmp/workspace --repo ./demo
./aoa goal --path /tmp/workspace "Add a greeting function"
./aoa run  --path /tmp/workspace
./aoa status --path /tmp/workspace
```

The `mock` Backend (`internal/agent/mock.go`) makes the **entire loop hermetic and offline** — the test
suite never makes network calls. Always keep it that way: tests must pass with no API keys.

## Repository map

| Path | Responsibility | Notes when changing |
|------|----------------|---------------------|
| `pkg/api` | Event envelope + typed payloads | Adding an event → add the payload type and a `state.Apply` case |
| `internal/ledger` | Append-only JSONL Event Log | Keep `Append` concurrency-safe |
| `internal/state` | Replay events → state, Task Graph readiness | Pure functions only; no I/O |
| `internal/orchestrator` | The Scheduler (single control loop) | Keep dispatch decoupled from the Merge Queue |
| `internal/agent` | `Backend` interface + CLI harness presets (`cli.go`: claudecode, codex, cursor, gemini, grok) + native `openai`/`anthropic` + `mock` | A CLI harness is a preset row, not a file (ADR 014); keep `mock` deterministic |
| `internal/worktree` | Git repo + isolated worktrees | All git calls use the config-independent identity helper |
| `internal/verify` | The Gate (objective verification) | Pure command runner; no orchestration logic |
| `internal/mergequeue` | Verify → merge → rollback (+ disjoint-file batching) | Must leave `main` green and linearizable |
| `internal/metrics`, `internal/diagnose` | Replay projections: run metrics + MAST failure-mode histogram | Pure functions of the event log; no instrumentation |
| `internal/otel` | Replay projection to OpenTelemetry (OTLP traces+metrics) | Off by default; never in the hot path; never networks in tests (ADR 012) |
| `internal/bench`, `internal/liveeval` | Hermetic coordination benchmark + live eval harness | `liveeval` is networked only with a networked Backend (ADR 009) |
| `internal/config` | `aoa.toml` loading | Add a field → set a default in `Default()` |
| `cmd/aoa` | Tiny stdlib CLI | No CLI framework dependency; document new commands in `docs/cli.md` |
| `scripts/` | Eval + benchmark harnesses (SWE-bench, live smoke, OTLP smoke) | Not covered by `make check`; keep them runnable from a clean clone |

## Conventions & Go Best Practices

- **Go style:** standard `gofmt`; doc comment on every exported symbol and package; table-driven tests.
- **TDD:** every change ships with a test. Mock external processes; use `t.TempDir()` for I/O. Tests must run offline.
- **Naming:** Short for short-lived vars (`i, n, err, ok`). No stuttering (`user.UserID` → `user.ID`). Acronyms should be consistent (`userID`, `httpClient`). Interfaces end in `-er` (`Reader`, `Writer`).
- **Interfaces:** Accept interfaces, return concrete types. Define interfaces at the call site, not the implementation.
- **Errors:** Always handle errors explicitly — never assign to `_`. Wrap with context (`fmt.Errorf("context: %w", err)`). Use `errors.Is()` / `errors.As()` for checking. Use custom error types for structured errors. Surface errors, don't swallow them.
- **Commits:** Conventional Commits, subject ≤72 chars. Do **not** put AI model names in commit subjects/bodies (the `Co-Authored-By` trailer is the only AI attribution).
- **Worktree/branch:** `main` is protected and a global post-checkout hook reverts the workspace root to `main`, so do feature work in a **`git worktree`** off `main`, commit there, push the branch, open a PR (one PR per increment). Rebase-merge. If a stacked PR conflicts after its base merges, rebase it onto `origin/main` (the base commit is auto-skipped) and force-push.

## AI Agent Behavioral Guidelines

When acting as an AI pair programming assistant for this backend project, abide by the following:

### 1. Think Before Coding
- **Don't assume. Don't hide confusion. Surface tradeoffs.**
- Before implementing: State your assumptions explicitly. If uncertain, ask.
- If multiple interpretations exist, present them - don't pick silently.
- If a simpler approach exists, say so. Push back when warranted.
- Analyze the query, consider broader implications, and plan the approach comprehensively before writing code.

### 2. Simplicity First
- **Minimum code that solves the problem. Nothing speculative.**
- No features beyond what was asked. No abstractions for single-use code.
- No "flexibility" or "configurability" that wasn't requested.
- Explain trade-offs, and prioritize scalability, reliability, and security over cleverness.
- Ask yourself: "Would a senior engineer say this is overcomplicated?" If yes, simplify.

### 3. Surgical Changes
- **Touch only what you must. Clean up only your own mess.**
- When editing existing code: Don't "improve" adjacent code, comments, or formatting.
- Don't refactor things that aren't broken.
- Match existing style perfectly.

### 4. Ponytail (Lazy Senior Dev Mode)
- **The best code is the code never written.** Stop and ask if a change needs to be built at all (YAGNI).
- **Leverage existing tools.** Does the standard library, a native platform feature, or an already-installed dependency cover it? If so, use it. Pick the edge-case-correct option when two approaches are similar.
- **Boring over clever.** No boilerplate nobody asked for. Deletion over addition. Fewest files possible. Can this be one line? Make it one line.
- **Question complex requests.** ("Do you actually need X, or does Y cover it?")

## Common tasks

- **Add a CLI harness:** add a row to `cliPresets` in `internal/agent/cli.go` (binary, args, whether it
  reports usage) plus a row in `TestCLIPresetArgv`, and a page under `docs/harnesses/`. Only add a case
  to `parseCLIOutput` if the CLI has its own output envelope. The prompt is appended as the **final
  argv element**, so a harness taking it as a flag value puts that flag last (ADR 014). No Go change is
  needed at all for a harness a user can describe with `[backends.<name>] type = "cli"`.
- **Add a non-CLI Backend:** implement `agent.Backend` (`Name`, `Run`), register it in
  `cmd/aoa/main.go:buildBackendSingle`, document it in `docs/harnesses/` and `README.md`.
- **Add a lifecycle event:** add the constant + payload in `pkg/api/events.go`, handle it in
  `internal/state/state.go:Apply`, emit it from `internal/orchestrator`, and cover it with a test.
- **Change the Gate:** edit `verify` in `aoa.toml`; the Merge Queue verifies the post-merge state.
- **Add observability (metric/span):** extend the replay projection in `internal/otel` (and
  `internal/metrics` for a new number) — never add a span/metric call into the orchestrator or ledger.
  Keep it behind `Enabled()` so offline runs and tests stay silent.

## Driving `aoa` from a harness

`aoa` is a CLI, so any agent harness that can run commands can drive it — no plugin, no server. The
usage contract lives in [`.claude/skills/aoa/SKILL.md`](.claude/skills/aoa/SKILL.md): when to reach for
it, how to submit and watch a Goal, and what to report back. Claude Code loads that file automatically;
for Codex, Cursor or anything else, read it as the instructions for using this tool.

Short version: `aoa doctor` before blaming a failure, `aoa goal` then `aoa run`, `aoa status` for what
happened. A merge means *the Gate passed* — say that, rather than that the change is good.
