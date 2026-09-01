---
name: aoa-design-decisions
description: Use before making a structural change to aoa — adding a dependency or external service, a new control loop or coordination mechanism, changing how state is stored, or answering "should aoa do X". Explains what the design already refused and why, and how to record a decision that contradicts one.
---

# Structural changes to aoa

`aoa` is opinionated, and every opinion is written down. A change that contradicts one is not
automatically wrong — but it needs a **new ADR** explaining why, not a quiet edit.

Read [`docs/design/architecture.md`](../../../docs/design/architecture.md) for the shape of the system, and
the ADR index at [`docs/design/adr/README.md`](../../../docs/design/adr/README.md) for the 14 decisions.
Read the ADR your change touches **before** writing code — most structural questions are already answered
there, usually with the evidence that settled them.

## The standing refusals

These have been considered and rejected. Proposing one again means arguing with a specific ADR:

| Not this | Because | ADR |
|---|---|---|
| A mutable store, cache or side table beside the log | all state is a replay of the Event Log | 001 |
| Multi-agent voting, debate or consensus as the merge decision | the Gate decides; agent opinion does not | 002, 005, 011 |
| A role hierarchy, a second control loop, or an LLM coordinator | coordination is deterministic Go, one Scheduler | 003, 013 |
| A provider SDK or CLI called from business logic | everything goes through `agent.Backend` | 004, 014 |
| Agent-to-agent messaging | agents coordinate by emitting `TicketCreated` to the shared log | 006 |
| Instrumentation inside the control loop | observability is a replay projection, off by default | 012 |
| A test that needs an API key or network | the suite is hermetic on the `mock` backend | 009 |

Debate and markets are not banned as *tools* — they are banned from the live control plane. ADR 011 says
where they are still allowed (offline analysis).

## Adding a dependency

One static binary, one config file, git only. The OpenTelemetry SDK (ADR 012) is the single sanctioned
third-party cluster, and it stays isolated in `internal/otel` and opt-in. Anything else needs a strong
justification in the PR and probably an ADR: prefer the standard library, and prefer a few lines to a
module.

## Writing a new ADR

1. Copy the shape of a recent one — `docs/design/adr/014-cli-backends-as-data.md` is a good short model:
   title, **Status**, **Context**, **Decision**, **Consequences**.
2. Number it next in sequence, kebab-case filename.
3. Add a row to the table in `docs/design/adr/README.md` (one line, what was decided).
4. If it supersedes or extends an earlier ADR, say so in both.
5. Cite evidence where the decision rests on it, and say plainly when it rests on ordinary engineering
   judgement instead — `docs/design/reading-list.md` maps each source to the claim it supports.

New pages under `docs/` also need a `nav:` entry — see the `aoa-docs-site` skill.
