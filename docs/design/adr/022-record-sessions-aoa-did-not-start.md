# ADR 022: `aoa` Records Agent Sessions It Did Not Start

## Status
Accepted. Extends [ADR 001](001-event-sourced-truth.md) (a second Event Log, in the repository) and
[ADR 015](015-aoa-is-a-backend.md) (what a caller may write). Reverses nothing.

## Context
The way people run coding agents today is several interactive sessions at once, one per git worktree,
started by hand from a terminal or an IDE. A developer running four of them does not lose track of
whether the model can write code; they lose track of *what each session did*: which files it changed,
whether it rewrote a test on the way, whether the tree is green, and which of the four has been idle
for an hour.

`aoa` could answer all of that — it already models a change as an Event Log and a Gate — but only for
work it dispatched itself. Adopting `aoa` therefore meant giving up the interactive session, which is
the part of the workflow people like. So the ledger nobody has was the one `aoa` was closest to having
and did not offer.

Two things in the existing design look like they forbid this, and do not:

- **ADR 015 gives front doors the authority of a human at the CLI** — submit, amend, approve, reject,
  cancel, read. Recording an observation is none of those, and it is weaker than all of them: nothing
  is dispatched, verified or merged because a session was observed. The Scheduler never reads these
  events.
- **"No daemon"** stands. Observation is a one-shot command the user runs, not a watcher. There is no
  second control loop, so [ADR 003](003-flat-orchestrator-worker.md) is untouched.

## Decision
**`aoa sessions` records what git shows of every linked worktree of a repository, whether `aoa` started
it or not, and `aoa sessions check` records whether the Gate passes on one of them.**

1. **A session is a linked git worktree.** The main working tree is the integration checkout, not a
   session; it supplies the default base instead. A session's identity is derived from its path, so
   every observation of one worktree folds into one session.
2. **Observation reads only.** `git worktree list`, `diff`, `ls-files`, `log`, and the modification
   times of the files git reports as changed. It never writes to a working tree, never prunes, never
   commits.
3. **The log lives in the repository, at `<git-common-dir>/aoa/events.jsonl`.** No workspace and no
   `aoa.toml` are needed, every worktree of the repo shares the one log, and nothing lands in any
   working tree. This is a second Event Log of the same format and the same rules, not a side table:
   the view is a pure fold of it (ADR 001).
4. **An observation is appended only when it says something new** — a session that appeared, changed,
   or whose worktree has gone. The fold and the decision happen under the ledger lock, so repeated
   runs and concurrent ones do not duplicate.
5. **`SessionChecked` is a report, not a merge decision.** It records that the Gate passed or failed
   on a tree at a given fingerprint. It merges nothing, and it says nothing about the integration
   branch, which only the merge queue may change (ADR 002).
6. **A verdict is pinned to content.** Each observation carries a fingerprint of HEAD, the uncommitted
   patch and the untracked files. A check recorded against an older fingerprint is shown as stale
   rather than as a verdict on what is there now.
7. **Touched test files are recorded as a field of the observation.** An agent that edits the tests it
   is judged by is the thing worth seeing at a glance; it is a property of the diff, not a separate
   event.

## Consequences
- **`aoa` is useful before it is adopted.** A developer keeps their own sessions and gets the ledger,
  which is the smallest honest first step toward the Gate.
- **The scope stays small.** No daemon, no chat, no dashboard, no vendor log parsing: git is the only
  source, so a harness that changes its own log format cannot break this.
- **The classifier is a heuristic.** Test paths are matched by convention across a handful of
  ecosystems. It will miss a project that names tests some other way; making it configurable is a
  later increment, not a new decision.
- **Two logs now exist in a workspace-plus-repo setup.** The workspace's log is `aoa`'s own work; the
  repository's is what its worktrees did. They are not merged, and nothing in the Scheduler reads the
  second.
- **Tradeoff.** `aoa` now records work it cannot vouch for. A session's own commits never went through
  the Gate on the post-merge state, and this ADR does not pretend otherwise — the ledger's value is
  that it says so.

## Research basis
Engineering judgement, plus the working pattern it is built for: parallel worktree-per-agent sessions
are the default in current harnesses, and review, not generation, is where the time goes.
