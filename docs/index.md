# Age of Agents

**Point an AI coding agent at your repo and it might write something broken. `aoa` runs the agent in a
throwaway git worktree, runs *your* build and tests on the result, and merges it only if they pass.**

It is a merge queue whose author happens to be a language model — Bors for AI agents. One static binary,
one config file, git only: no database, no broker, no service to run.

```
$ aoa goal "add table-driven tests for parseUsage"
$ aoa run

  [merged  ] g-45973ca0-impl  (attempts=2 tokens=606,669)
  total: tokens=606,669  wall=343.7s
  all work settled
```

<div class="grid cards" markdown>

- :material-rocket-launch: **[Get started](getting-started.md)** — from clone to a merged change, offline, in about ten seconds
- :material-download: **[Install](install.md)** — `go install`, a release binary, or from source
- :material-robot: **[Harnesses](harnesses/README.md)** — drive Claude Code, Codex, Cursor, Grok, Gemini, or any CLI you already have
- :material-tune: **[Configuration](config-reference.md)** — every `aoa.toml` field, with defaults

</div>

## Why it exists

Most agent frameworks chase orchestration cleverness — role hierarchies, debate, consensus — which is
exactly the part that better models erode. `aoa` bets the other way: **verification, not intelligence,
is the scaling constraint.**

Scheduling, state, merge and done-ness are plain deterministic Go, gated on objective signals: your
build, your tests, your compiler. The LLM only ever emits a *candidate diff*; whether it lands is
decided by your Gate, not by the agent. Better models sharpen the worker; the control plane is unchanged.

## The shape of it

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

Everything is an event, and all state is a replay of the log — so crash recovery, audit trails and every
metric come for free rather than being built.

## What it does not do

No agent-to-agent messaging, no voting, no debate, no LLM coordinator, no role hierarchy. Those are
[deliberate refusals](design/adr/README.md), each with an ADR saying why.

## Where things stand

The loop closes end to end on real repositories, with real backends, on real money — with cost
accounting, spend governors and OpenTelemetry export. What is **not** yet established is whether the
Gate changes outcomes at scale: the SWE-bench numbers recorded so far were produced with the Gate
*disabled*. The project says so [in its own README](https://github.com/bharadwaj6/ageOfAgents#readme)
rather than quoting a headline solve-rate it cannot stand behind.
