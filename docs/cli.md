# CLI reference

Every command, every flag. `aoa --help` prints a shorter version of this; `aoa <command> --help` prints
one command's synopsis, example and flags.

## Conventions

**Flags come before positional text.** Go's `flag` package stops at the first non-flag argument, so a
flag written after the text is silently swallowed into it:

```bash
aoa goal --path ./ws "fix the parser"     # correct
aoa goal "fix the parser" --path ./ws     # rejected, with an explanation
```

**`--path DIR` selects the workspace** and defaults to `.` — except `aoa quickstart`, which defaults to
`./workspace` because it creates one. Four commands take no `--path` at all, because they don't read a
workspace: `bench`, `eval`, `version` and `completion`.

## Getting started

### `aoa quickstart`

Scaffold a workspace, submit a goal and run it — `init` → `goal` → `run` → `status` in one command,
offline on the `mock` backend. Each step is printed as it runs. Refuses to run over a workspace that
already exists.

| Flag | Default | |
|---|---|---|
| `--path DIR` | `./workspace` | workspace root to create |
| `--goal TEXT` | `add a greeting function` | the goal to submit |

### `aoa init`

Scaffold a new workspace, or adopt a repo you already have.

| Flag | Default | |
|---|---|---|
| `--path DIR` | `.` | workspace root |
| `--repo PATH` | `./repo` | integration repo to scaffold (demo mode) |
| `--adopt PATH` | — | adopt an existing git repo at this path, on its current branch, and auto-detect its Gate |
| `--force` | `false` | overwrite an existing `aoa.toml` |

`--adopt` writes nothing into the target repo. Gate detection: `go.mod` → `go build`/`go test`,
`package.json` → `npm test`, Python → `pytest`, a `Makefile` → `make test`.

### `aoa doctor`

Check that a workspace can actually run, before a run proves it can't. Verifies git, the workspace,
`aoa.toml`, the repo, the configured backend **and every fallback**, each Gate command's binary, docker
when `sandbox = "docker"`, and that the Event Log replays. Every failure prints the one action that
fixes it. **Exits non-zero**, so CI can gate on it.

| Flag | Default | |
|---|---|---|
| `--path DIR` | `.` | workspace root |

A dirty integration repo is a warning, not a failure — Workers branch from `HEAD` and will not see
uncommitted changes.

## Running work

### `aoa goal`

Submit a Goal. Takes the objective as positional text.

```bash
aoa goal --path ./ws "add table-driven tests for parseUsage"
```

### `aoa run`

Run the Scheduler. By default it reconciles until all work settles, then exits `0`. Safe to re-run at
any time — a settled workspace does no work. See [Scheduling](scheduling.md).

| Flag | Default | |
|---|---|---|
| `--path DIR` | `.` | workspace root |
| `--once` | `false` | a single reconcile pass instead of looping |
| `--interval D` | `0` | keep reconciling every `D` until interrupted (`0` = run until settled, then exit) |
| `--otel` | `false` | after the run, replay the Event Log to OTLP |
| `--otel-live` | `false` | stream spans to OTLP live, as events happen |

Both OTel flags need `OTEL_EXPORTER_OTLP_ENDPOINT` — see [Observability](integrations/README.md).

### `aoa amend`

Append steering guidance to a Goal mid-run. Future dispatches pick it up; the attempt already in flight
does not.

```bash
aoa amend --path ./ws g-45973ca0 "keep the public API unchanged"
```

### `aoa approve` · `aoa reject`

Decide a proposal parked by the approval gate (`require_approval = true`). Takes a ticket id.

| Flag | Default | |
|---|---|---|
| `--path DIR` | `.` | workspace root |

## Inspecting

### `aoa status`

Goals, task states, attempts, per-ticket tokens, run cost, and a "needs human" handoff naming the
preserved worktree for each failure.

| Flag | Default | |
|---|---|---|
| `--path DIR` | `.` | workspace root |
| `--watch` | `false` | re-render until all work settles |
| `--interval D` | `2s` | refresh interval for `--watch` |

### `aoa events`

Inspect the Event Log — the append-only record every other number is derived from.

```bash
aoa events --path ./ws tail --count 20
aoa events --path ./ws replay --type TicketMerged
```

| Flag | Default | |
|---|---|---|
| `--path DIR` | `.` | workspace root |
| `--count N` | `20` | events to show for `tail` (`0` = all) |
| `--type T` | — | filter by event type |

Subcommands: `tail` (default) and `replay`. `aoa feed` is a deprecated alias for `events tail`.

### `aoa diagnose`

A MAST-style failure-mode histogram for a run — where attempts died, grouped by cause.

| Flag | Default | |
|---|---|---|
| `--path DIR` | `.` | workspace root |
| `--json` | `false` | emit JSON instead of a markdown table |

### `aoa otel export`

Replay a finished Event Log to OTLP traces and metrics, post hoc. Off by default; observability is a
replay projection, never hot-path instrumentation ([ADR 012](design/adr/012-observability-as-replay-projection.md)).

| Flag | Default | |
|---|---|---|
| `--path DIR` | `.` | workspace root |

## Evaluation and benchmarking

### `aoa eval`

Run end-to-end tasks against real repos and report per-task success, tokens and `$`. **No `--path`** —
it reads a task file instead of a workspace.

| Flag | Default | |
|---|---|---|
| `--tasks F` | — | TOML task file (required) |
| `--backend B` | `mock` | `mock`, `claudecode`, `codex`, `cursor`, `gemini`, `grok`, `openai`, `anthropic`, or a configured plugin |
| `--json` | `false` | emit JSON instead of a markdown table |
| `--price P` | `0` | flat USD per million tokens (`0` = unpriced) |
| `--price-file F` | — | TOML `[pricing]` file, model → USD/Mtok, for per-model cost |
| `--max-cost $` | `0` | stop launching tasks once cumulative `$` crosses this ceiling (`0` = no cap) |
| `--otel` | `false` | export each task's Event Log to OTLP |

See [Live evaluation](design/live_eval.md).

### `aoa bench`

The hermetic coordination benchmark. Offline, no workspace, no `--path`.

| Flag | Default | |
|---|---|---|
| `--json` | `false` | emit JSON instead of a markdown table |

## Serving

### `aoa serve`

A GitHub webhook server: an `@aoa <goal>` issue comment queues a Goal.

| Flag | Default | |
|---|---|---|
| `--port N` | `8080` | port to listen on |
| `--path DIR` | `.` | workspace root |
| `--secret S` | — | GitHub webhook secret |

!!! warning "Always set `--secret`"
    Without it, anyone who can reach the port can queue work that runs an agent on your machine. See
    [Scheduling](scheduling.md) and [`SECURITY.md`](https://github.com/bharadwaj6/ageOfAgents/blob/main/SECURITY.md).

## Shell integration

### `aoa version`

Print the version, commit and build date, plus Go version and OS/arch. No flags.

### `aoa completion`

Print a shell completion script. Takes one positional argument: `bash`, `zsh` or `fish`.

```bash
aoa completion zsh  > "${fpath[1]}/_aoa"   # then: compinit
aoa completion bash > /etc/bash_completion.d/aoa
aoa completion fish > ~/.config/fish/completions/aoa.fish
```
