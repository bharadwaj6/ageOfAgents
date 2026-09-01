# Age of Agents

[![CI](https://github.com/bharadwaj6/ageOfAgents/actions/workflows/ci.yml/badge.svg)](https://github.com/bharadwaj6/ageOfAgents/actions/workflows/ci.yml)
[![Docs](https://img.shields.io/badge/docs-bharadwaj6.github.io-blue.svg)](https://bharadwaj6.github.io/ageOfAgents/)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8.svg)](go.mod)

**Run a fleet of coding agents against one repo, unattended, and come back to a `main` that still
builds.**

Every agent gets a throwaway git worktree. Their output is serialised through a merge queue that runs
*your* build and tests on the **merged** result, and rolls back anything that breaks. The agent
proposes. It never decides.

```console
$ aoa goal "add rate limiting to the API"
$ aoa goal "migrate the auth tests to table-driven"
$ aoa goal "fix the flaky worker shutdown test"
$ aoa run

  [merged  ] g-638386cc-impl  (attempts=1 tokens=48,210)
  [merged  ] g-e9e3c987-impl  (attempts=2 tokens=131,884)
  [failed  ] g-4e7cf92f-impl  (attempts=2 tokens=96,004)

needs human — failed tickets:
  g-4e7cf92f-impl — gate failed: go test ./... (2 attempts)
      take over: cd ./workspace/.aoa/worktrees/aoa-g-4e7cf92f-impl-ea0391f7

total: tokens=276,098  wall=412.6s
merge queue: max-depth=3  wait-mean=0.2s  wait-max=1.4s
all work settled
```

Two landed, one didn't, and the one that didn't never touched `main` — its worktree is sitting there
for you. One static binary, one config file, git only: no database, no broker, no service to run.

## Install

```bash
curl -fsSL https://raw.githubusercontent.com/bharadwaj6/ageOfAgents/main/scripts/install.sh | sh
```

macOS and Linux, amd64 and arm64. On Windows, or with Go 1.26.4+ already installed:

```bash
go install github.com/bharadwaj6/ageOfAgents/cmd/aoa@latest
```

Release binaries, from-source builds and shell completions:
[Install](https://bharadwaj6.github.io/ageOfAgents/install/).

## Quickstart

The whole loop, offline, in about ten seconds — no API key, no cost:

```bash
aoa quickstart --path ./workspace
```

That wraps four ordinary commands (`init` → `goal` → `run` → `status`) and prints each as it runs. The
default backend is `mock` — a **fixture, not a tiny model** — so what you just exercised is the real
machinery (isolated worktree → your Gate → serialised merge → the Event Log) with nothing to pay for.
Pick a real agent below to get real code.

When something looks wrong, run `aoa doctor --path ./workspace` before you debug: it checks the things
that otherwise fail deep inside a run — a missing backend CLI, a Gate command not on `$PATH`, an Event
Log that won't replay — and prints the fix for each.

## How it works

```mermaid
flowchart LR
    Goal(["Goal"]) --> Log[("Event Log<br/>(append-only JSONL)")]
    Log -.->|replay| Sched{"Scheduler<br/>(deterministic Go)"}
    Sched -->|append| Log
    Sched ==>|dispatch| W[["Worker<br/>(agent in an isolated worktree)"]]
    W -->|candidate diff| Log
    Sched ==>|drive| MQ[/"Merge Queue<br/>verify → merge → roll back"\]
    MQ -->|your build + tests| Main(["main, always green"])
```

A Goal becomes a Task, dispatched to a Worker in its own worktree. The Worker emits a *candidate diff*;
the Merge Queue merges it, runs your Gate on the post-merge state, and keeps it only if the Gate passes.
Everything is an event and all state is a replay of the log, so crash recovery, audit trails and every
metric come for free. Coordination is plain deterministic Go: no LLM in the control plane, no agents
messaging each other, no voting — [deliberate refusals](https://bharadwaj6.github.io/ageOfAgents/design/adr/),
each with a decision record saying why.

If you're supervising one agent on one change, you don't need any of this — just tell it to run the
tests. `aoa` is for the case where that stops working:
[three things a prompt can't do](https://bharadwaj6.github.io/ageOfAgents/#three-things-a-prompt-cant-do).

## Point it at your own repo

```bash
aoa init --path ./workspace --adopt /path/to/your/repo
```

Writes nothing into your tree, leaves it on its current branch, and sniffs a starting Gate from the
project (`go.mod` → `go build`/`go test`, `package.json` → `npm test`). It never provisions your test
environment — it runs the Gate you configure, on the machine you run it on. Edit `verify` in
`workspace/aoa.toml` if the guess is wrong.

> **Security.** An agent backend runs commands the model chooses, on your machine, with your
> permissions. `sandbox = "docker"` isolates the **Gate**, not the agent. Read [`SECURITY.md`](SECURITY.md)
> before pointing a real backend at anything you care about.

## Bring your own agent

| `backend` | Needs | Reports cost? | Status |
|---|---|---|---|
| `mock` | nothing | n/a | offline fixture; every hermetic test runs on it |
| `claudecode` | the `claude` CLI | ✅ | verified |
| `codex` | the `codex` CLI | ✅ | **verified end to end** |
| `cursor` | the `cursor-agent` CLI | ❌ | flags verified; no live run |
| `grok` | the `grok` CLI (grok.com login, no API key) | ✅ | verified |
| `gemini` | the `gemini` CLI | ❌ | unverified |
| `openai` · `anthropic` | the matching API key | ✅ | native HTTP; not verified against the live API |
| **anything else** | your CLI | ❌ | `type = "cli"` in `aoa.toml` — no Go code |

*Verified* means a real Goal went through that backend to a merge. Where a harness reports no token
counts, `aoa` says so at startup rather than quietly showing you `$0`. Setup, flags and the reasoning
behind each: [Harnesses](https://bharadwaj6.github.io/ageOfAgents/harnesses/).

## Where things stand

The loop closes end to end on real repositories, with real backends, on real money — with cost
accounting, spend governors and OpenTelemetry export. What is **not** established is whether the Gate
changes outcomes at scale: every SWE-bench number recorded so far was produced with the Gate *disabled*,
so those runs measure the backend agent, not the merge queue. The numbers and their caveats are in
[live evaluation](https://bharadwaj6.github.io/ageOfAgents/design/live_eval/).

The rule itself isn't new: Graydon Hoare called it the
[not-rocket-science rule](https://graydon2.dreamwidth.org/1597.html) — *automatically maintain a
repository of code that always passes all the tests* — and wrote `bors` to enforce it for Rust. `aoa`
applies it to a fleet of authors that are faster, cheaper, more numerous, and considerably more
confident than they have earned.

## Docs

**[bharadwaj6.github.io/ageOfAgents](https://bharadwaj6.github.io/ageOfAgents/)**

| | |
|---|---|
| First run, explained step by step | [Get started](https://bharadwaj6.github.io/ageOfAgents/getting-started/) |
| Every command, flag and `aoa.toml` field | [CLI](https://bharadwaj6.github.io/ageOfAgents/cli/) · [Configuration](https://bharadwaj6.github.io/ageOfAgents/config-reference/) |
| Why it's built this way | [Architecture](https://bharadwaj6.github.io/ageOfAgents/design/architecture/) + [decision records](https://bharadwaj6.github.io/ageOfAgents/design/adr/) |
| Contributing | [`CONTRIBUTING.md`](CONTRIBUTING.md) · [`CHANGELOG.md`](CHANGELOG.md) |

MIT licensed.
