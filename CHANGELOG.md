# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`aoa quickstart`** — scaffold a workspace, submit a goal and run it in one command, offline on the
  mock backend. It is a wrapper over the four real commands, each printed as it runs, so what you have
  just watched is a script you can retype.
- **`aoa doctor`** — check that a workspace can actually run before a run proves it can't: git present,
  `aoa.toml` parses, `repo` is a git repository, the configured backend (and every fallback) is usable
  on this machine, each Gate command's binary resolves, docker is present when `sandbox = "docker"`,
  and the Event Log replays. Every failure prints the one action that fixes it. Exits non-zero, so CI
  can gate on it.
- **`aoa completion bash|zsh|fish`** — static completion scripts, no CLI framework. A test scans the
  dispatch switch in `main()` and fails if a subcommand is added without being completed.

- **Four more harnesses, and a door for the rest.** `codex`, `cursor` and `gemini` join `claudecode` and
  `grok` as built-in backends, and `[backends.<name>] type = "cli"` drives any coding-agent CLI with no
  Go code ([ADR 014](docs/design/adr/014-cli-backends-as-data.md), [docs/harnesses/](docs/harnesses/README.md)).
  DeepSeek needs no backend at all — it is an OpenAI-compatible endpoint, which already worked.
  A CLI backend is now a table row rather than a file: `claudecode.go` and `grok.go` were line-for-line
  copies, and three more would have been three more copies.
- **A skill for driving `aoa` from an agent harness** (`.claude/skills/aoa/SKILL.md`), pointed at from
  `AGENTS.md`. `aoa` is a CLI, so any harness that can run commands can already drive it — what was
  missing was the contract for *when* to reach for it and what to report back. One file; Claude Code
  loads it automatically, Codex and Cursor read it via `AGENTS.md`. No MCP server.
- **A documentation site** at [bharadwaj6.github.io/ageOfAgents](https://bharadwaj6.github.io/ageOfAgents/),
  built from `docs/` with MkDocs Material and published on every push to `main`. CI builds it with
  `--strict`, so a broken cross-reference fails the build rather than rotting quietly.
- `aoa` warns at startup when `max_tokens_per_goal`/`max_usd_per_goal` are set on a backend that reports
  no token usage. The governors were silently inert on such backends.

### Fixed

- **`max_passes` was an accidental wall-clock timeout.** A reconcile pass spent purely *waiting* on an
  in-flight worker consumed the pass budget, so the defaults (1000 passes x 100ms `poll_interval`)
  capped a run at roughly 100 seconds of agent time. Every real backend exceeds that: a live `codex`
  run took 183s and died with "orchestrator exceeded 1000 passes" while holding a perfectly good
  proposal, which merged on a second `aoa run`. Waiting no longer counts as a pass; the bound still
  stops a run that keeps emitting events without converging.
- `aoa help` documented neither `init --adopt`/`--force`, `status --interval`, `eval --price`/`--json`,
  nor `serve --secret` — the last of which `docs/scheduling.md` says you must always set or anyone who
  can reach the port can queue work.

## [0.3.0] — 2026-08-24

### Changed

- **Dispatch is a worker pool, not a wave** ([ADR 013](docs/design/adr/013-worker-pool-not-dispatch-wave.md)).
  `ReconcileOnce` used to launch up to `concurrency` workers, block on `wg.Wait()`, and only then drain
  the merge queue. Three consequences, all gone:
  - `concurrency` was a **batch size**, not a steady-state pool — a worker that finished early left its
    slot idle until every sibling finished.
  - **The slowest agent in a batch blocked every merge in it**, bounded only by `agent_timeout` (30m).
    A proposal ready in one minute could wait half an hour for the queue to look at it.
  - The Stall Detector could never observe a running worker, because it ran after the barrier.

  Dispatch is now asynchronous across reconcile passes; `Run` tops the pool up each pass, drains the merge
  queue every pass, and joins its workers before returning on every path including errors. **The merge
  queue is unchanged and still the single serialized writer to `main` — ADR 002 is untouched.**

  Pinned by a test asserting a fast ticket merges while a slow sibling is still running: it passes in
  0.26s, and hangs into its 30s guard when the barrier is restored.

### Added

- `poll_interval` (default `100ms`) — how long `aoa run` waits between passes while workers are busy.

### Fixed

- Two races the change exposed and the existing suites caught immediately: `Run` reporting a stall while a
  worker was still starting, and the same ticket being dispatched twice because the log did not yet show
  it claimed. Both came from `Ticket.ActiveWorkers` lagging a launched goroutine; dispatch is now
  reconciled against the goroutines actually running, taking the larger of the two counts so a worker
  orphaned by an earlier crash still counts.
- Dependencies: OpenTelemetry family to 1.45.0, `actions/setup-go` to v7.

## [0.2.0] — 2026-08-24

The release where the claims and the code were made to agree.

`v0.1.0` shipped roughly fifteen features in a single day and claimed a SWE-bench result it could not
support. This release retracts that claim, fixes the defects that a real run exposed, and makes the
repository usable by someone who has never seen it. **No new features.**

### Verified

- **The loop closes end to end on a real repository.** Two runs on 2026-08-23 with the `grok` backend
  against a real Go repo: one merged a correct change on the first attempt (105s); one merged on the
  second attempt with the full `go build && go vet && go test ./...` suite as the Gate (344s, 606,669
  tokens). `main` green after both; the full suite passed post-merge; re-running a settled workspace does
  no work and exits `0`. **Two tasks are not a solve-rate** — this is evidence the loop closes, and no
  more than that.

### Removed

- **The "50% pass@1 on a verified 20-instance SWE-bench Lite subset" claim.** No such run existed — the
  largest on disk was 11 instances — and every recorded run had the **Gate disabled** (`--inference-mode`
  set the Gate to a no-op), so those numbers measured the backend agent, not the verifier-gated merge
  queue the project is about. The README also contradicted itself about this on the same page.
- **`aoa compact` and `Ledger.Compact`.** Compaction rewrote the log to a single `StateSnapshot` that
  `metrics`, `diagnose` and `otel` all read as **zeros** and the invariant checker read as a violation —
  silently. It is not a fixable bug: a snapshot carries no attempt history, so replay-derived metrics and
  a compacted log are mutually exclusive by construction. The event type remains, so existing logs stay
  readable.
- **`internal/chatops`** — 65 lines of Slack/Teams stubs that only logged, with zero importers.
- **Orphaned gVisor scripts** the README advertised but no code path ever used.

### Fixed

- **`main` could be left mid-merge while the queue reported a clean rejection.** `worktree.Merge` ran
  `git merge --abort` inside an empty `if` that discarded the error, and the merge queue trusted it
  without verifying. The queue now restores the pre-merge HEAD itself and reports a failure to do so as an
  error. This was the one path that could break the invariant the whole product rests on.
- **Nothing in the codebase had a timeout.** A wedged agent CLI hung the run forever, and the Stall
  Detector could not help because it runs after the dispatch wave joins. New `agent_timeout` (default
  30m) bounds every attempt.
- **Cost accounting was fiction, twice.** `parseUsage` looked for a fence that `BuildPrompt` never
  requested, so `grok` and `claudecode` reported **0 tokens** on every real run and the spend governor,
  `--max-cost` and every `$` column were inert. Both now read their CLI's `--output-format json` for true
  counts. Then, once real numbers appeared, `aoa status` reported **289,946 tokens against 606,669 spent**
  — the governor charged failed attempts and the display did not.
- **A sandbox crash was recorded as a verdict against the patch.** Both Gate rejections in the first
  precision sweep were spurious; one patch was byte-identical to one that had already resolved. Docker's
  reserved exit codes now mark `Result.Infra`.
- **`aoa run` exited `0` when every task failed**, making every scheduling recipe unalertable.
- **Dispatch discarded its own failures** — including a completed diff thrown away over a bookkeeping
  append, and `decompose` leaving a ticket claimed-and-running forever after a state-load error.
- **An abandoned attempt recorded no reason**, so retries re-ran an identical prompt and crash-loop
  detection could never fire on anything but a Gate rejection.
- **`aoa eval` had no in-run spend ceiling** and silently ignored the configured backend, evaluating
  `mock` while reporting a clean run.
- **`aoa init` inherited the user's global git hooks**, so a failing `pre-commit` hook would reject every
  commit `aoa` makes, surfacing only as "agent produced no changes".
- **The Docker sandbox defaulted to `golang:1.22`** against a `go 1.26.4` module.
- CLI traps: `aoa events tail --count N` ignored `--count`; `aoa goal "text" --path X` silently submitted
  the flag as part of the goal; a mistyped `--path` created the directory; a missing backend CLI was
  discovered only after the retry budget was spent; four `config.Load` errors were discarded.

### Added

- **`aoa version`**, stamped at release time — published binaries could not previously identify themselves.
- **MIT `LICENSE`**, `CONTRIBUTING.md`, `SECURITY.md`, issue and PR templates, `.editorconfig`,
  Dependabot — none of which existed.
- **`make install`**, and per-subcommand `--help` that describes the command with an example instead of
  dumping bare flag names.
- **CI**: race-detector job and macOS/Windows builds (GoReleaser ships both; nothing compiled them).
- `agent_timeout` and `sandbox_image` config fields; `--gate=none|f2p|repo` in the SWE-bench adapter, so
  the merge Gate can be varied independently of the oracle.

### Changed

- **README restructured** so the first runnable command is line 35 rather than line 141, with
  prerequisites, a diagram, and the thesis moved below the practical content.
- Raw LLM research transcripts (172 KB) moved to `docs/research/` behind a disclaimer. `docs/claude.md`
  was renamed — on a case-insensitive filesystem it shadowed `CLAUDE.md`, so Claude Code silently loaded
  it as project instructions.
- Commits written by agents now have readable subjects; previously the first line was the entire goal
  text plus a ticket id.

### Known limitations

- The **agent is not sandboxed**. `sandbox = "docker"` isolates the Gate, not the agent. See
  [`SECURITY.md`](SECURITY.md) and [#101](https://github.com/bharadwaj6/ageOfAgents/issues/101).
- **Gate precision is unmeasured** — [#103](https://github.com/bharadwaj6/ageOfAgents/issues/103).
- The `openai` and `anthropic` backends have **never been run against the live APIs**.

## [0.1.0] — 2026-06-16

First tagged release: event-sourced ledger, verifier-gated merge queue, deterministic scheduler, git
worktree isolation, `mock`/`claudecode`/`grok`/`openai`/`anthropic` backends, OpenTelemetry export,
Docker sandboxing for the Gate, GitHub Actions integration, and a TLA+ model of the merge invariants.

**Its release notes claim a SWE-bench result that this project cannot support.** See the 0.2.0 entry.

[0.3.0]: https://github.com/bharadwaj6/ageOfAgents/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/bharadwaj6/ageOfAgents/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/bharadwaj6/ageOfAgents/releases/tag/v0.1.0
