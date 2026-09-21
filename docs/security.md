# Security

!!! danger "Read this before pointing a real backend at anything you care about"

    `aoa` exists to run **code a language model wrote**, and then run **your build and tests** on the
    result. Both are arbitrary code execution, by design.

    **`aoa` does not confine the agent.** Unless the harness you chose confines itself, the agent runs
    as your user, with your files, your credentials and your network. `sandbox = "docker"` does not
    change this — it containerises the *Gate*, not the agent.

    Run `aoa` with a real backend on a machine where you would run untrusted code.

This page is the detail. The repository's
[`SECURITY.md`](https://github.com/bharadwaj6/ageOfAgents/blob/main/SECURITY.md) carries the same summary
plus the vulnerability reporting policy.

## What actually runs, and as whom

A backend is the only part of `aoa` that executes something a model chose. What confines it is a
property of the **harness**, not of `aoa`:

| `backend` | How `aoa` invokes it | What confines it |
|---|---|---|
| `mock` | in-process fixture | nothing to confine — it runs no command a model chose, and never networks |
| `codex` | `codex exec --json --sandbox workspace-write` | **the harness's own OS sandbox.** codex restricts writes to the workspace and disables network by default |
| `claudecode` | `claude --permission-mode acceptEdits …` | file edits are auto-approved; no OS-level confinement |
| `grok` | `grok --permission-mode bypassPermissions …` | nothing — permission prompts are bypassed wholesale |
| `cursor` | `cursor-agent -p --force --trust …` | nothing — `--force` allows any command not explicitly denied |
| `gemini` | `gemini --approval-mode yolo …` | nothing — `yolo` is the approval mode's off switch |
| `openai`, `anthropic` | an in-process loop exposing a `bash` tool | nothing |
| your own `type = "cli"` | your binary, your `args` | whatever your CLI does on its own |

Two things follow from that table:

- **`codex` is the only preset that ships confinement**, and it is codex's, not `aoa`'s. `aoa` neither
  configures nor verifies it, and overriding `args` under `[backends.codex]` in `aoa.toml` can remove it.
- The permissive flags on the others are **deliberate and load-bearing**, not an oversight. A headless
  `claude -p` with no permission mode runs, declines to write any file, and every Task fails with
  "agent produced no changes". The reasoning is recorded next to each preset in
  [`internal/agent/cli.go`](https://github.com/bharadwaj6/ageOfAgents/blob/main/internal/agent/cli.go).

## The worktree is a working directory, not a boundary

Every agent gets a throwaway git worktree, and that is genuinely useful: it isolates agents *from each
other* and keeps unmerged work off `main`. It is not a security boundary.

The `openai` and `anthropic` backends run the model's commands like this:

```go
cmd := exec.CommandContext(ctx, "bash", "-c", args.Command)
cmd.Dir = task.Worktree
```

`cmd.Dir` sets the directory the command *starts* in. Nothing keeps it there. The command can `cd /`,
read `~/.ssh`, write anywhere your user can write, open a network connection, or `git push`.

## The agent inherits your environment

None of the backends set `cmd.Env`, so every agent subprocess inherits the environment `aoa` was started
with. If `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `GITHUB_TOKEN` or your cloud credentials are exported in
that shell, they are visible to the agent process. The CLI harnesses additionally read their own
credential stores from your home directory, which is how they are authenticated at all.

Scope credentials to what the task actually needs, and prefer a shell that does not carry your
long-lived tokens.

## `sandbox = "docker"` isolates the Gate, not the agent

This is the single most common misreading, so it is worth being precise about what the setting does.

With `sandbox = "docker"`, each Gate command runs as:

```
docker run --rm -v <repo>:/workspace -w /workspace <sandbox_image> <verify command>
```

What that gets you: your `verify` commands do not run directly on your host, and they run against a
declared toolchain rather than whatever happens to be on your `$PATH`.

What it does **not** get you:

- **It never touches the agent.** The agent ran before the Gate did, on the host, unconfined.
- **It is not a jail for hostile code.** The repository is mounted read-write, the container has network,
  and it runs as the image's default user — root, for the default `golang:1.26`. A `docker run` with a
  writable bind mount is a boundary against *accidents on your host*, not against something trying to
  get out.

## The Gate runs unreviewed code before you decide to keep it

`sandbox = ""` (the default) runs your `verify` commands directly on the host. The Gate verifies the
**post-merge** state, so a proposed diff is executed as part of verification — before you have decided
anything about it. If a proposal can edit your test scripts, it can run whatever those scripts run.

Setting `sandbox = "docker"` and a `sandbox_image` is worth doing for exactly this reason, with the
limits above in mind.

## What to do about it

`aoa` is honest about this rather than fixed, and
[ADR 018](design/adr/018-the-agent-is-not-confined.md) records why. In practice:

- **Run it somewhere disposable** — a VM, a container, a dedicated box, a CI runner. This is the one
  measure that actually works, and it is the same advice that applies to any coding agent.
- **Scope the credentials** in the environment you launch `aoa` from.
- **Use `codex`** if you want harness-level confinement without building your own.
- **Set `sandbox = "docker"`** so the Gate's commands at least do not run on your host.
- **Set budgets** (`max_usd_per_goal`, `[budget] usd_per_day`, `agent_timeout`). A runaway loop is a
  real cost, and on an unattended run the budget is the only thing that stops it. See
  [Configuration](config-reference.md).
- **Use `require_approval`** when you want a human between the proposal and the merge.
- **Don't run it against a repository whose history you cannot restore.**

`aoa doctor` prints this posture as a warning before your first real run:

```console
$ aoa doctor --path ./workspace
  ...
  ok    backend:claudecode     /usr/local/bin/claude (login not checked)
  warn  confinement            none — claudecode runs as your user, with your files, credentials and network
        this is by design; read SECURITY.md and run real backends somewhere you would run untrusted code
```

It is a warning, not a failure: this is the documented design, so it does not break `aoa doctor` in CI.

## `aoa serve` is unauthenticated without a secret

The webhook server accepts an `@aoa <goal>` issue comment and queues real agent work — which, per
everything above, means running an agent on your machine.

- `--secret` enables HMAC verification of GitHub's signature. Without it the server warns and starts
  anyway; that is a convenience for local testing, not a deployment posture.
- `--secret` authenticates **GitHub**, not the commenter. Who may queue work is `--allow`, a list of
  GitHub `author_association` values defaulting to `OWNER,MEMBER,COLLABORATOR`.
- **Widening `--allow` to `CONTRIBUTOR`, `FIRST_TIME_CONTRIBUTOR`, `FIRST_TIMER` or `NONE` lets strangers
  run agents on your machine** — on a public repository, anyone can become a contributor by getting one
  pull request merged.

See [Scheduling](scheduling.md) and the
[CLI reference](cli.md).

## What is verified, and what isn't

- The test suite is hermetic and offline; the `mock` backend never makes a network call.
- A fault-injection suite and a TLA+ model check the merge and approval invariants.
- The `openai` and `anthropic` backends have **never been verified against the live APIs** — they are
  exercised only against stub servers.
- The `cursor` and `gemini` presets' flags were read off the vendors' help output, not confirmed by a
  live run.

## Reporting a vulnerability

This is a personal project with no security team and no SLA. The policy, including how to report
something you would rather not post publicly, is in
[`SECURITY.md`](https://github.com/bharadwaj6/ageOfAgents/blob/main/SECURITY.md).
