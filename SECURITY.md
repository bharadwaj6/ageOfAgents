# Security

## What `aoa` does to your machine

Be clear-eyed about this before pointing it at anything you care about. `aoa` exists to run
**code written by a language model** and then run **your build and tests** on the result. Both are
arbitrary code execution, by design.

### `aoa` does not confine the agent

An agent backend runs commands the model chooses, as your user, with your permissions and your
credentials. What confinement exists is a property of the **harness**, not of `aoa`:

| `backend` | How `aoa` invokes it | What confines it |
|---|---|---|
| `mock` | in-process fixture | runs no command a model chose; never networks |
| `codex` | `codex exec --json --sandbox workspace-write` | **the harness's own OS sandbox** — codex sandboxes model-generated shell commands; `workspace-write` permits writes to the workspace. codex's policy, not `aoa`'s, and `aoa` does not verify it |
| `claudecode` | `claude --permission-mode acceptEdits …` | file edits auto-approved; no OS-level confinement |
| `grok` | `grok --permission-mode bypassPermissions …` | nothing |
| `cursor` | `cursor-agent -p --force --trust …` | nothing; `--force` allows anything not explicitly denied |
| `gemini` | `gemini --approval-mode yolo …` | nothing |
| `openai`, `anthropic` | in-process loop exposing a `bash` tool | nothing |
| your own `type = "cli"` | your binary, your `args` | whatever your CLI does |

The permissive modes are deliberate: without them a headless run writes no files and every Task fails
with "agent produced no changes".

**The worktree is not a boundary.** `openai` and `anthropic` run `exec.CommandContext(ctx, "bash", "-c", …)`
with `cmd.Dir` set to the task's worktree. That is where the command *starts*; nothing holds it there. The
model can `cd /`, read `~/.ssh`, reach the network, or `git push`.

**The agent inherits your environment.** No backend sets `cmd.Env`, so every key and token exported in the
shell you launched `aoa` from is visible to the agent process.

**`sandbox = "docker"` isolates the Gate, not the agent.** It containerises your `verify` commands. It does
nothing about the agent that produced the diff. Even for the Gate it is a boundary against host accidents
rather than a jail: the repo is bind-mounted read-write, the container has network, and the default image
runs as root.

Treat a machine running `aoa` with a real backend the way you would treat a machine running any untrusted
code: prefer a container, a VM, or a dedicated box; scope credentials to what the task needs; and do not
run it against a repo whose history you cannot restore. `aoa doctor` prints this posture as a `confinement`
warning before your first real run.

This is a decision, not an oversight —
[ADR 018](https://bharadwaj6.github.io/ageOfAgents/design/adr/018-the-agent-is-not-confined/) records why,
and the [Security page](https://bharadwaj6.github.io/ageOfAgents/security/) has the full detail.

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

`--secret` authenticates **GitHub**, not the commenter: a correctly signed delivery can still carry a
comment from anyone who can comment on the repository, which on a public repository is everyone. Who may
queue work is decided by `--allow`, a list of GitHub `author_association` values, defaulting to
`OWNER,MEMBER,COLLABORATOR` — people with write access or membership of the owning organization. Any
other `@aoa` comment is answered `200 ignored`, queues nothing, and is logged with the commenter's login.
**Widening `--allow` to `CONTRIBUTOR`, `FIRST_TIME_CONTRIBUTOR`, `FIRST_TIMER` or `NONE` lets strangers run
agents on your machine** — anyone can become a contributor by getting one pull request merged. The
allowlist is only as trustworthy as the payload: without `--secret`, `author_association` is whatever the
sender wrote.

### `aoa ui` has the CLI's authority, so it stays on this machine

The web view can submit, amend and cancel goals and approve or reject parked proposals, which is
everything `aoa goal`, `amend`, `cancel`, `approve` and `reject` can do. It has no login. By default it
listens on `127.0.0.1` and answers only requests addressed to a loopback hostname, which defeats DNS
rebinding. It also refuses writes that are not a same-origin JSON request, so a web page on another site
cannot drive it. A non-loopback `--addr` is refused unless `--read-only` is set, and even a read-only
view shows every goal and its Gate output to anyone who can reach the port. To use it from another
machine, tunnel the port over SSH. See [ADR 021](docs/design/adr/021-a-local-web-view-is-the-cli-in-a-browser.md).

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
