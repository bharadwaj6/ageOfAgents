# Contributing to Age of Agents

Thanks for looking. This file is for **humans**. [`AGENTS.md`](AGENTS.md) is the same material written for
AI coding agents; if you are one, read that instead.

## Before you write code

`aoa` is opinionated on purpose, and the opinions are written down as
[ADRs](docs/design/adr/). A change that contradicts one is not automatically wrong — but it needs a **new
ADR** explaining why, not a quiet edit. The six that shape everything:

1. **The Event Log is the single source of truth.** All state is a replay of `pkg/api` events. New
   behaviour = a new event type + a `state.Apply` case, never a side table. (ADR 001)
2. **Nothing merges that fails the Gate.** Correctness comes from your build, tests and linter — not from
   agent opinion. No voting, no debate, no consensus quorums. (ADR 002, 005)
3. **Coordination is deterministic Go; only the work is done by LLMs.** There is exactly one Scheduler. No
   role hierarchies, no second control loop, no LLM coordinator. (ADR 003)
4. **All LLM access goes through `agent.Backend`.** Never call a provider SDK or CLI from business logic.
   (ADR 004)
5. **Agents coordinate through the shared log, not messages.** Extend the graph by emitting
   `TicketCreated`. (ADR 006)
6. **Keep it small and portable.** One static binary, one config file, git only. A new dependency or a
   required external service needs strong justification.

## The vocabulary gap

The docs and the code use different words for the same thing. This trips up everyone once:

| Docs say | Code says | Where |
|---|---|---|
| Task | `Ticket` | `internal/state`, `pkg/api` |
| Scheduler | `Orchestrator`, `ReconcileOnce` | `internal/orchestrator` |
| Event Log | `Ledger` | `internal/ledger` |
| Gate | `Verifier`, `verify.Command` | `internal/verify` |
| Merge Queue | `mergequeue.Queue` | `internal/mergequeue` |
| Replay | `state.Fold` | `internal/state` |
| Worker | a goroutine running `agent.Backend.Run` | `internal/orchestrator` |

## Getting set up

```bash
git clone https://github.com/bharadwaj6/ageOfAgents.git && cd ageOfAgents
make check      # build + vet + test + gofmt — takes ~5 min, must be green
make help       # every other target
```

You need **Go 1.26.4+** and `git`. Nothing else: the whole test suite is hermetic and offline, because the
`mock` backend never touches the network. **Keep it that way** — a test that needs an API key is a test
that will not run in CI.

Running one package while you work:

```bash
go test ./internal/mergequeue/ -run TestProcess -v
```

## Making a change

- **Every change ships a test**, and the test should **fail without the fix**. Verify that — stash the fix
  and watch it go red. Several bugs in this repo shipped with tests that never exercised them.
- **One PR per increment.** `main` is push-protected; work on a branch and open a PR.
- **Run `make check` before every commit** and read its output. Do not pipe it through `tail` and trust
  the exit code — a pipeline reports its *last* command's status, which is how a red branch got merged
  here once.
- **Conventional Commits**, subject ≤72 chars: `feat:`, `fix:`, `refactor:`, `docs:`, `chore:`.
- Explain **why** in the commit body, not what — the diff already says what.

## Style

Standard `gofmt` (`make fmt`). Doc comment on every exported symbol. Table-driven tests. Use `t.TempDir()`
for I/O. Wrap errors with context (`fmt.Errorf("context: %w", err)`) and **never** assign one to `_` — this
repo has been bitten repeatedly by swallowed errors, most memorably a discarded `git merge --abort` failure
that could leave `main` mid-merge while the queue reported a clean rejection.

Accept interfaces, return concrete types. Interfaces end in `-er`. No stuttering (`user.ID`, not
`user.UserID`).

## Common tasks

- **Add a backend:** implement `agent.Backend` (`Name`, `Run`), register it in
  `cmd/aoa/main.go:buildBackendSingle`, add a preflight check if it shells out to a CLI, and document it in
  `README.md` and `docs/integrations/`.
- **Add a lifecycle event:** add the constant + payload in `pkg/api/events.go`, handle it in
  `internal/state/state.go:Apply`, emit it from `internal/orchestrator`, and cover it with a test. If it
  carries token usage, charge it in **both** `internal/state` and `internal/metrics` — they read the same
  log and have drifted apart before.
- **Add a metric:** extend the replay projection in `internal/metrics` or `internal/otel`. Never
  instrument the control loop.

## Reporting bugs

Open an issue. A failing test, or the `aoa events tail` output around the problem, is worth more than a
description — the Event Log is a complete record of what the system did.

For anything with a security dimension, read [`SECURITY.md`](SECURITY.md) first.
