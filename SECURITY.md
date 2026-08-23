# Security

## What `aoa` does to your machine

Be clear-eyed about this before pointing it at anything you care about. `aoa` exists to run
**code written by a language model** and then run **your build and tests** on the result. Both are
arbitrary code execution, by design.

### The agent is not sandboxed

An agent backend runs commands the model chooses, as your user, with your permissions and your
credentials:

- `claudecode` and `grok` shell out to their CLIs with permissive modes (`acceptEdits`,
  `bypassPermissions`) so the headless agent can actually write files.
- `openai` and `anthropic` expose a `bash` tool and run what the model asks for via
  `exec.CommandContext(ctx, "bash", "-c", …)`. The working directory is the task's worktree, but **nothing
  confines the command to it.** The model can `cd` elsewhere, read `~/.ssh`, or reach the network.

**`sandbox = "docker"` isolates the Gate, not the agent.** It containerises your `verify` commands. It does
nothing about the agent that produced the diff.

Treat a machine running `aoa` with a real backend the way you would treat a machine running any untrusted
code: prefer a container, a VM, or a dedicated box; scope credentials to what the task needs; and do not
run it against a repo whose history you cannot restore.

### The Gate runs on the host by default

`sandbox = ""` (the default) runs your `verify` commands directly. If a proposal can modify your test
scripts, it can run whatever those scripts run. The Gate verifies the **post-merge** state, so a malicious
diff is executed as part of verification, before you have decided to keep it.

Set `sandbox = "docker"` and a `sandbox_image` when the Gate's commands should not touch the host.

### `aoa serve` is an unauthenticated endpoint without a secret

The webhook server accepts an `@aoa <goal>` issue comment and queues real agent work. `--secret` enables
HMAC verification of GitHub's signature. **Without it, anyone who can reach the port can make your machine
run an AI agent against your repository.** The server warns and starts anyway; that is a convenience for
local testing, not a deployment posture. Always set `--secret` on anything reachable.

### Cost is a security property here

A runaway loop spends real money. `max_tokens_per_goal` and `max_usd_per_goal` are per-goal circuit
breakers, and `agent_timeout` bounds a single wedged attempt. They only work on backends that report
usage — currently `grok`, `claudecode`, `openai` and `anthropic`. Set them before an unattended run.

## What is verified, and what isn't

- The test suite is hermetic and offline; the `mock` backend never makes a network call.
- A fault-injection suite and a TLA+ model check the merge/approval invariants.
- The `openai` and `anthropic` backends have **never been verified against the live APIs** — they are
  exercised only against stub servers.

## Reporting a vulnerability

This is a personal project with no security team and no SLA. Open a GitHub issue for anything low-risk. For
something you would rather not post publicly, contact the repository owner directly through GitHub and
allow a reasonable window before disclosure.

Please do include: what you did, what happened, and the `aoa events tail` output if the Event Log captured
it.
