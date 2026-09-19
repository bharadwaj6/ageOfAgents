# ADR 015: `aoa` is a Backend — Front Doors Decide What, `aoa` Decides Whether It Lands

## Status
Accepted. Extends [ADR 003](003-flat-orchestrator-worker.md) (what counts as a coordinator),
[ADR 008](008-human-in-the-loop-approval-gate.md) (who may approve) and
[ADR 010](010-semantic-idempotency.md) (idempotency keys supplied from outside).

## Context
Agentic engineering has moved on from one agent in one terminal. Tools now take work from a task board
or a chat message and carry it through to a merged change with little human attention:

- **firstmate** is a dispatcher agent that runs a crew of harnesses and relays requests from Discord or X.
- **OpenAI Symphony** is a daemon that polls Linear or GitHub Issues and runs one Codex session per issue.
- Others include the Copilot coding agent, Linear's agent sessions and Gas Town.

`aoa` could follow them upward by growing its own chat intake, board sync, LLM triage and dashboard.
Doing that would mean competing with these tools on their own ground, and relaxing ADR 003 to let an
LLM decide what the fleet works on.

Those systems stop at the point where `aoa` is strongest. Symphony's spec describes itself as "a
scheduler/runner and tracker reader". It specifies no verification or merge gate, and a run ends at a
workflow handoff state such as `Human Review`. firstmate states its boundary outright: "firstmate is the
command layer, not the workshop: validation belongs to no-mistakes, CI belongs to the forge, and merge
policy belongs to the configured authority." Neither tool guarantees the thing `aoa` exists for. That
guarantee is that a change lands only after an objective Gate passes on the **post-merge** state, with
isolation, idempotency, budgets and an audit trail that replays.

Driving `aoa` from another program is fragile today. Every command except `bench`, `eval` and `diagnose`
prints prose, so a caller has to scrape it. A goal does not record where it came from. There is no way
to withdraw one. The Event Log also cannot take two concurrent writers: an `aoa goal` issued while
`aoa run` is live can write a duplicate sequence number.

## Decision
**`aoa` is the gated execution backend. A *front door* decides what gets worked on; `aoa` decides
whether that work lands.**

1. **Front doors own the "what".** Discovery, triage, intent, prioritisation and the conversation with
   the requester all belong to the front door. A front door may be a person at a terminal, a CI job, a
   board poller, a chat bot, or an LLM orchestrator such as firstmate. Front doors may use LLMs.
2. **`aoa` owns the "whether".** It provides worktree isolation, the Gate, the merge queue, the approval
   gate, budgets, retries, and the Event Log as the audit trail.
3. **A front door has exactly the authority of a human at the CLI, and no more.** It can submit, amend,
   approve, reject and cancel, and it can read. It cannot dispatch, reorder, merge or skip the Gate.
   An LLM front door therefore is **not** a coordinator in ADR 003's sense: nothing it does reaches
   inside the control loop, and nothing it says can make failing work land. ADR 003's refusal is
   scoped to `aoa`'s own control plane.
4. **`aoa` builds no triager and no LLM intake.** This closes the discovery gap in
   [loop engineering](../loop_engineering.md). The front door owns discovery, so `aoa` does not grow a
   discovery mechanism of its own.
5. **The backend contract.** These are the stable, machine-readable verbs front doors build on. They
   are delivered as a stack of increments tracked in the [roadmap](../roadmap.md):

   | Verb | Output | Guarantee |
   |---|---|---|
   | `aoa goal --json --source S --ref R --key K` | `{schema, goal_id, duplicate, seq}` | Submitting the same key twice returns the same goal, even when submitters race. A duplicate is not appended. |
   | `aoa status --json` | goals with origin, outcome, tickets and cost | A pure projection. The text `status` renders the same view. |
   | `aoa events --json --since N [--follow]` | ledger lines, byte for byte | A resumable cursor that never emits a partial line. |
   | `aoa approve` / `reject` / `amend` / `cancel` with `--json` | `{schema, …, seq}` | The check and the append are atomic under the ledger lock. |
   | `aoa run` exit codes | `0` ok · `1` failed · `2` usage · `75` busy | Exactly one Scheduler per workspace. |

   Result types live in `pkg/api`, the only importable package, under a `schema` version that only
   ever grows by addition.
6. **Many submitters, one Scheduler.** Any number of front doors may write to the Event Log at once;
   appends are serialised across processes. Only one Scheduler may reconcile a workspace, and an OS
   lock enforces this. It used to be a line in the docs.
7. **Approval records who, but cannot prove a human.** `approve --by` is recorded on the log. `aoa`
   cannot tell a person from a bot holding the same CLI. An operator who wants ADR 008's gate to mean
   *a human looked* must not give `approve` to an automated front door.
8. **Goal text from a front door is untrusted agent input.** It is bounded by the same controls as any
   other goal: the Gate, the sandbox, the budgets and the approval gate.
9. **"Lands" means the workspace's integration branch, for now.** Pushing to a remote or opening a PR
   is the next thing teams need, and it changes what the Gate guarantees. Delivery therefore gets its
   own ADR ([#128](https://github.com/bharadwaj6/ageOfAgents/issues/128); decided in
   [ADR 016](016-deliver-a-goal-as-a-pull-request.md)).

Where the systems named above sit relative to `aoa`. These are positions, not claims about how the
systems work internally:

| System | Position |
|---|---|
| firstmate | Front door. A crewmate can drive `aoa` through its CLI, and a check script can poll `status --json`. |
| Symphony-style board runners | Front door. `aoa` can be the per-issue executor and supply the gate the spec leaves out. |
| Copilot coding agent | A combined front door and executor. It complements `aoa` when you want your own Gate, log and harness. |
| Linear agent sessions | Front door. Delegating an issue becomes `aoa goal --ref <issue>`. |
| no-mistakes | A complementary gate at push time. It sits downstream of `aoa`'s future delivery modes, not in place of the Gate. |

## Consequences
- **No existing ADR is reversed.** The single deterministic Scheduler, the Gate as the only merge
  authority, the Event Log as truth and "no daemon" all stand. The front-door tools are built on
  precisely the things those ADRs refuse. That makes them natural callers of `aoa` rather than
  competitors to absorb.
- **The scope stays small.** `aoa` does not grow chat integrations, a board UI or a triage model. What
  it gains is a contract: JSON verbs, a resumable event cursor, cancellation, and safety for concurrent
  writers.
- **The contract becomes a compatibility surface.** Its result types carry a `schema` version, and a
  golden wire-shape test turns a renamed field into a failing test.
- **Later increments get their own records.** These are delivery by push or PR (ADR 016, #128),
  reporting back ([#129](https://github.com/bharadwaj6/ageOfAgents/issues/129)), a team HTTP transport
  for the same verbs ([#76](https://github.com/bharadwaj6/ageOfAgents/issues/76)), and reference
  integrations ([#130](https://github.com/bharadwaj6/ageOfAgents/issues/130)).
- **Tradeoff.** `aoa` alone is not an end-to-end automator. A team wanting board-to-PR automation
  needs a front door in addition to `aoa`. Being a component rather than a whole product is the price
  of keeping the control plane deterministic.

## Research basis
Mostly engineering judgement about where to draw a system boundary, not evidence from a paper. The
factual claims about other systems are drawn from their own documents: Symphony's
[SPEC](https://github.com/openai/symphony/blob/main/SPEC.md) ("a scheduler/runner and tracker reader";
runs end at a handoff state) and firstmate's
[VISION](https://github.com/kunchenguid/firstmate/blob/main/VISION.md) ("the command layer, not the
workshop"). The case for keeping the merge decision objective is ADR 002's, and it is unchanged.
