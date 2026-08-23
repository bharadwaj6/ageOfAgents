# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
- `concurrency` is a dispatch **wave size**, not a worker pool —
  [#102](https://github.com/bharadwaj6/ageOfAgents/issues/102).

## [0.1.0] — 2026-06-16

First tagged release: event-sourced ledger, verifier-gated merge queue, deterministic scheduler, git
worktree isolation, `mock`/`claudecode`/`grok`/`openai`/`anthropic` backends, OpenTelemetry export,
Docker sandboxing for the Gate, GitHub Actions integration, and a TLA+ model of the merge invariants.

**Its release notes claim a SWE-bench result that this project cannot support.** See the 0.2.0 entry.

[0.2.0]: https://github.com/bharadwaj6/ageOfAgents/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/bharadwaj6/ageOfAgents/releases/tag/v0.1.0
