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

| Flag | Default | |
|---|---|---|
| `--path DIR` | `.` | workspace root |
| `--json` | `false` | print the result as one JSON line (see [Machine-readable output](#machine-readable-output)) |
| `--key K` | — | idempotency key: submitting the same key again appends nothing and returns the Goal it already names |
| `--source S` | `human` | the entry point submitting it — a front door's name, `ci`, … |
| `--ref R` | — | where the Goal came from: a URL or a tracker reference such as `linear:ENG-123` |
| `--by B` | — | who asked for it, recorded as given |

`--key` is what makes a front door safe to retry. A board poller can re-submit the same issue every cycle
with `--key linear:ENG-123`: the first call creates the Goal, every later one prints
`goal g-… already submitted (key "linear:ENG-123")` and writes nothing. Concurrent submitters of one key —
separate processes included — agree on a single Goal. `--source`, `--ref` and `--by` are stored on the
Goal so whoever reports back on it knows where to.

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

**One Scheduler per workspace.** A run holds an OS lock on `.aoa/scheduler.lock` while it reconciles, and
a second `aoa run` on the same workspace refuses rather than racing it. Goals already on the log are
not lost: the run holding the lock reconciles them before it finishes (a `--once` run leaves them to the
next run). With `--interval`, a pass that finds the workspace busy is skipped and the loop carries on.

| Exit status | Meaning |
|---|---|
| `0` | all work settled and no task failed |
| `1` | a task failed, or the run hit an error |
| `2` | a flag could not be parsed |
| `75` | another `aoa run` holds the workspace (`EX_TEMPFAIL`); nothing was done |

### `aoa amend`

Append steering guidance to a Goal mid-run. Future dispatches pick it up; the attempt already in flight
does not.

```bash
aoa amend --path ./ws g-45973ca0 "keep the public API unchanged"
```

| Flag | Default | |
|---|---|---|
| `--path DIR` | `.` | workspace root |
| `--json` | `false` | print the result as one JSON line |

An unknown goal id is an error, and appends nothing.

### `aoa approve` · `aoa reject`

Decide a proposal parked by the approval gate (`require_approval = true`). Takes a ticket id.

| Flag | Default | |
|---|---|---|
| `--path DIR` | `.` | workspace root |
| `--json` | `false` | print the result as one JSON line |
| `--by B` | — | who decided, recorded as given |
| `--reason R` | — (`reject`: `rejected by operator`) | why, recorded with the decision |

Repeating a decision already made — approving an approved ticket, rejecting a rejected one — succeeds,
says `already approved`/`already rejected`, and appends nothing, so a retry is safe. Contradicting one
(rejecting an approved ticket) is an error, as is deciding a ticket that is not awaiting approval.

### Machine-readable output

With `--json`, `goal`, `amend`, `approve` and `reject` print exactly one line of JSON to stdout, and
nothing else. The shapes are the result types in
[`pkg/api/contract.go`](https://github.com/bharadwaj6/ageOfAgents/blob/main/pkg/api/contract.go):

```json
{"schema":1,"goal_id":"g-1a2b3c4d","duplicate":false,"seq":7}
{"schema":1,"goal_id":"g-1a2b3c4d","seq":12}
{"schema":1,"ticket_id":"g-1a2b3c4d-impl","decision":"approved","seq":20,"already_decided":false}
```

`seq` is the sequence number of the event written — or, for a duplicate submit (`"duplicate":true`) or
a repeated decision (`"already_decided":true`), of the original event, since nothing new was written.
`schema` is the contract version: within a version fields are only ever added, so ignore any you do not
know; renaming or removing one bumps it. Errors still go to stderr with a non-zero exit.

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

Inspect the Event Log — the append-only record every other number is derived from. It is also how a
program driving `aoa` follows what happened: `--json`, `--since` and `--follow` read the log as a
resumable JSONL stream.

```bash
aoa events --path ./ws tail --count 20
aoa events --path ./ws replay --type Merged
aoa events --path ./ws --json --since 41    # every event after seq 41, one JSON object per line
aoa events --path ./ws --json --since 41 --follow    # ...then each new event as it is appended
```

| Flag | Default | |
|---|---|---|
| `--path DIR` | `.` | workspace root |
| `--count N` | `20` | events to show for `tail` (`0` = all); cannot be combined with `--since` |
| `--type T` | — | print only events of type `T` |
| `--json` | `false` | print each event as its Event Log line, byte for byte |
| `--since N` | — | print every event with a seq greater than `N` |
| `--follow` | `false` | then keep printing events as they are appended, until interrupted |
| `--poll D` | `500ms` | how often `--follow` checks the log |

Subcommands: `tail` (default) and `replay`, which adds each event's payload. `aoa feed` is a deprecated
alias for `events tail`.

#### Reading the log from a program

`--json` prints the log's own lines, unmodified: one JSON object per line, the event envelope
(`seq`, `type`, `ts`, `actor`, `payload` — see
[`pkg/api/events.go`](https://github.com/bharadwaj6/ageOfAgents/blob/main/pkg/api/events.go)). The
bytes are copied, not re-encoded, so a field a newer `aoa` adds to the envelope reaches you even through
an older binary.

`--since N` is the cursor. It prints *every* event whose seq is greater than `N`, which is why it refuses
an explicit `--count` — a tail would silently drop events you have not seen. `tail` and `replay` select
the same events under `--since`; in text mode `replay` still adds the payload. To consume the log
incrementally:

1. Read once with `--since 0`.
2. Keep the `seq` of the last line you received.
3. Next time, pass that seq to `--since`. You get exactly the events appended since, none of them twice.

`--type T` filters what is printed, not the cursor, so filtered output skips the seqs of other events.
Resume from the last seq you *received*: every event you skipped was of another type.

`--follow` turns the read into a stream: after printing its selection, it checks the log every `--poll`
and prints each event appended since, in order, until interrupted (SIGINT or SIGTERM). It prints only
complete lines, never one a writer is part-way through, and polls without taking the log's lock, so
it never holds up `aoa run`. If it exits, restart it with `--since` set to the last seq you received and nothing is lost
or repeated. Should the log be truncated or replaced under it, `--follow` reads it again from the start
and skips every seq it has already passed.

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

A GitHub webhook server: an `@aoa <goal>` issue comment queues a Goal — if the commenter is trusted.
The Goal records the issue's URL as its ref and the commenter's login as who asked. It is keyed on the
delivery id, so a redelivery appends nothing.

| Flag | Default | |
|---|---|---|
| `--port N` | `8080` | port to listen on |
| `--path DIR` | `.` | workspace root |
| `--secret S` | — | GitHub webhook secret |
| `--allow LIST` | `OWNER,MEMBER,COLLABORATOR` | comma-separated `author_association` values that may queue work; case-insensitive |

`--allow` accepts GitHub's `author_association` values: `OWNER`, `MEMBER`, `COLLABORATOR`, `CONTRIBUTOR`,
`FIRST_TIMER`, `FIRST_TIME_CONTRIBUTOR`, `MANNEQUIN`, `NONE`. An unknown or empty value stops the server
at startup. An `@aoa` comment from anyone outside the list is answered `200 ignored` — not an error
status, which GitHub would redeliver — queues nothing, and logs one line naming the commenter.

!!! warning "Always set `--secret`, and keep `--allow` narrow"
    Without `--secret`, anyone who can reach the port can queue work that runs an agent on your machine,
    and can forge the `author_association` that `--allow` checks. With it, `--allow` decides who may queue
    work: adding `CONTRIBUTOR` or `NONE` lets strangers on a public repository run agents on your machine.
    See [Scheduling](scheduling.md) and [`SECURITY.md`](https://github.com/bharadwaj6/ageOfAgents/blob/main/SECURITY.md).

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
