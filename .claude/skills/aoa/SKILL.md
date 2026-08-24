---
name: aoa
description: Use when the user wants a code change made under a verification gate rather than applied directly — "run this through aoa", "have aoa do it", delegating a task to an isolated worktree, or checking on goals already submitted. aoa runs a coding agent in a throwaway git worktree and merges only if the build and tests pass.
---

# Running work through aoa

`aoa` is a merge queue for AI-authored changes. It runs a coding agent in a throwaway git worktree,
runs the project's build and tests on the result, and merges only if they pass. You drive it with
ordinary shell commands.

**Use it when the user wants a change gated rather than applied** — you keep working while it runs, and
whatever lands is known to build and pass tests. **Don't use it for edits you can just make**: a
one-line fix does not need a worktree, a Gate and a merge queue.

## Check the workspace first

```bash
aoa doctor --path <workspace>
```

Reports git, the config, the backend CLI, every Gate command, and whether the Event Log replays. Each
failure prints its fix. Exits non-zero. **Run this before blaming a failed goal on the agent.**

If there is no workspace yet:

```bash
aoa init --path <workspace> --adopt <path-to-repo>   # existing repo, Gate auto-detected
```

`--adopt` writes nothing into the target repo and leaves it on its current branch.

## Submit and run

```bash
aoa goal --path <workspace> "add table-driven tests for parseUsage"
aoa run  --path <workspace>
```

`run` reconciles until everything settles, then exits 0. It is safe to re-run at any time — re-running
a settled workspace does no work. A goal becomes exactly one task unless the agent itself decides to
decompose.

Write goals the way you would write a ticket: say what "done" means, and name the files if you know
them. The agent gets the goal text and the repo, nothing else from this conversation.

## Watch and report back

```bash
aoa status --path <workspace>              # goals, tasks, attempts, tokens, cost
aoa events --path <workspace> tail -n 20   # the log all of that is derived from
aoa diagnose --path <workspace>            # failure-mode histogram when things go wrong
```

`status` is the one to quote to the user. A failed task prints a "needs human" line with the worktree
path, so you can `cd` there and take over rather than starting again.

## Steering a run

```bash
aoa amend --path <workspace> <goal-id> "keep the public API unchanged"
```

Applies to future dispatches, not the attempt already in flight.

If `require_approval = true`, verified proposals park instead of merging:

```bash
aoa approve --path <workspace> <ticket-id>
aoa reject  --path <workspace> <ticket-id>
```

## What to tell the user

Report what actually happened, from `aoa status`: merged or failed, how many attempts, tokens and cost
if the backend reports them. If a task failed, give them the worktree path — the work is preserved
there, not discarded.

Don't claim a change is good because aoa merged it. Merging means *the Gate passed*, and the Gate is
only as good as the project's tests. Say which it was.

## Notes

- The backend is set by `backend` in `aoa.toml` — `claudecode`, `codex`, `cursor`, `grok`, `gemini`, an
  API backend, or any CLI via `type = "cli"`. Don't change it without asking.
- A run costs real money on a real backend. Check `max_usd_per_goal` before submitting something large,
  and note that governors are inert on backends that report no usage (`aoa doctor` says which).
- Goal text is passed to the agent as a single argument, never through a shell.
