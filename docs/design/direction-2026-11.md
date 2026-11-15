# Direction review, November 2026

`aoa` paused in late September on two negative results. This page records what the evidence says
before anything else is built. It proposes nothing as settled: a direction becomes a decision only
through a new [decision record](adr/README.md).

## Why this review exists

- **The Gate rejected nothing in the pilot.** The pre-registered run for
  [#103](https://github.com/bharadwaj6/ageOfAgents/issues/103) merged all 20 proposals
  ([live evaluation](live_eval.md), Amendment 2).
- **The sessions ledger was not used.** The two-week trial of `aoa sessions` ended with its kill signal
  fired ([roadmap](roadmap.md), Part 4; the outcome is recorded in
  [#240](https://github.com/bharadwaj6/ageOfAgents/pull/240)).
- **The maintainer's read.** The Gate and the Merge Queue are human process carried over. The
  primitives are neither accessible nor extensible. What limits a fleet is shared understanding, not
  verification. The intended user is one developer running several agent sessions a day.
- **The origin.** The project began from one claim: coordination for agents should be built from what
  agents are, not copied from structures that exist to manage human limits. The original prompt is in
  git history (`git show cac011b:docs/history/prompt.md`).

The review has three parts: what `aoa`'s mechanisms did in real use, what was missing when work went
wrong, and what each mechanism is for.

## What the mechanisms did in real use

Six workspaces were used to build `aoa` with `aoa` between 2026-09-19 and 2026-09-23. Their Event Logs
hold 763 events. The counts below come from the CLI and reproduce on any workspace:

```bash
aoa events --path WORKSPACE --json --since 0 | jq -r .type | sort | uniq -c
```

| What | Count |
|---|---|
| Goals submitted | 35 |
| Attempts started | 47 |
| Proposals | 28 |
| Merged and delivered | 24 |
| Gate rejections | 4 |
| Tasks that ended in failure | 11 |
| Budget stops | 5 |
| Goals amended while running | 2 |
| Goals cancelled | 3 |
| Goals with more than one Task | 0 |
| Dependency edges between Tasks | 0 |
| Approvals or rejections by a person | 0 |
| Rollbacks, or regressions caught by the Shadow set | 0 |

Three things follow.

**Decomposition, the Task Graph, the approval gate and rollback never ran.** Every Goal was one Task.
These mechanisms are tested and proven hermetically. In five days of real use nothing called on them.

**The Gate rejected four proposals, and one rejection was about the agent's work.**

| Rejection | What failed | Cause |
|---|---|---|
| A docs paragraph | Tests in another package: `cannot run gpg` | The clone had commit signing on and no `gpg`. Environment |
| A fix to error reporting | One budget test in another package | A flaky test. It passes 10 of 10 runs at the rejected commit |
| A new script | Two telemetry tests expecting no endpoint to be set | The workspace inherited telemetry variables from the shell. Environment |
| A new test script | The Python linter, one error in the new file | A defect in the agent's change |

With the pilot's 20, that is 48 proposals and one defect caught.

**Most failed Tasks were setup.** Six of the eleven ended with "agent produced no changes" on one
harness whose settings left it in a mode that never edits. Three were the result of a cancel. Two were
the harness exiting with an error, one of them over credentials.

## What was missing when work went wrong

### Method

The unit is an incident: work stopped, was redone, was corrected by a person, or produced a written
lesson. Each gets one code, the earliest thing that was missing. The scheme was written before any item
was coded.

| Code | What was missing |
|---|---|
| `INTENT` | What the person wanted, or what "done" meant |
| `DECISION` | A decision already made, or its reason |
| `CONVENTION` | How work is done in this repository or by this person |
| `OTHER-SESSION` | What another session or agent was doing or had changed |
| `ENVIRONMENT` | A fact about the machine, tool, harness, credentials or quota |
| `CHECK` | A correctness check that was absent, too weak, wrong or misread |
| `NONE` | Not an incident: a status record with no failure behind it |

There are 37 items from three sources: the 21 lessons the maintainer's agent sessions wrote down while
working on this repository, 9 incidents from the Event Logs above, and 7 incidents recorded in the
[roadmap](roadmap.md) and [live evaluation](live_eval.md) notes. One further Event Log failure is
excluded because its cause was never recorded.

A second coder, an agent with a fresh context given only the scheme and the items, coded all 37
independently.

### Result

| Code | First coder | Second coder |
|---|---|---|
| `ENVIRONMENT` | 10 | 11 |
| `CHECK` | 10 | 9 |
| `CONVENTION` | 5 | 6 |
| `OTHER-SESSION` | 3 | 3 |
| `INTENT` | 2 | 2 |
| `DECISION` | 2 | 1 |
| `NONE` | 5 | 5 |

The coders agreed on 35 of 37 items (Cohen's kappa 0.93). They split on whether a run that swapped the
harness between compared arms broke a decision or a convention, and on whether a container outage
recorded as a Gate verdict was an environment fault or a check fault.

Of the 32 incidents:

- **In 22, the agent lacked something that was known somewhere**: a fact about the environment (10), a
  convention (5), what another session had done (3), what the person meant (2), or a prior decision (2).
- **In 10, a check was at fault.** In six the check was wrong, flaky or misread, so it blocked good work
  or gave a false signal. In four it was missing or too weak, so wrong work or a wrong claim got
  through.

### What this does and does not show

- It supports the claim that missing knowledge stops work more often than missing verification does. It
  also says which knowledge: mostly plain facts about the machine and the tool, not intent.
- It supports the claim that the checks cost more than they caught in this record: six incidents where
  a check blocked or misled, against one defect the Gate caught.
- It does not support dropping checks. Four incidents are wrong work passing a weak or absent check.
- It cannot measure how often a person had to restate intent. Those moments live in session
  transcripts, and almost none survive for this repository.
- The sample is `aoa`'s own development, a project about checks, run in workspaces that needed a lot
  of setup. Both leading codes are over-represented by construction. This is a screen, not a rate.
- A check being wrong is not evidence that checks are unnecessary. Agents in these runs also ran the
  tests themselves before proposing, and nothing here counts what that caught.

## What each mechanism is for

Each mechanism was read in the code and in its decision record, and asked one question from the
origin prompt: whose limit does it manage? The classes are a human limit agents do not share, a limit
agents really have, a property of concurrency whoever the workers are, and a human need for
accountability.

| Mechanism | Whose limit | An agent in its own session can use it | A new kind needs code | Verdict |
|---|---|---|---|---|
| Event Log | Agents (no memory), concurrency | Read; write only through fixed verbs | Storage no, meaning yes | Keep |
| Replay | Concurrency | Read, through `status --json` | Yes | Keep |
| Goal and idempotency key | Agents (repeated work), concurrency | Yes | Yes | Keep |
| Task Graph and dependency edges | Concurrency (ordering) | No | Yes | Demote |
| Claim, dispatch, worker pool | Concurrency, token cost | No | Yes | Demote |
| Decomposition and its governor | Agents (context size, runaway output) | No | Limits no, shape yes | Demote |
| Proposal | Agents (unreliable self-assessment) | No | Yes | Keep only as the queue's input |
| Gate | Agents | Yes, with no workspace (`aoa sessions check`) | No, it is a list of commands | Keep |
| Merge Queue and rollback | Concurrency | No | Yes | Demote to something a session can call |
| Shadow verifier | Agents | Configuration only | No | Demote |
| Approval gate | Human accountability | Yes | Yes | Demote; already off by default |
| Budgets | Agents (cost), the person who pays | Read only | Limits no, scopes yes | Keep with dispatch |
| Retry feedback and context pack | Agents (no memory across attempts) | No | Yes | Demote |
| Stall detector and heartbeats | Concurrency (dead processes) | No | Timeouts no | Keep the detector |
| Worktree isolation | Concurrency | Observed, not requested | — | Demote; harnesses do it |
| Pull request delivery | Human accountability | Configuration only | Opener no, unit yes | Keep as optional |
| Sessions ledger | Human attention | Yes, with no workspace; text only | Yes | Demote |
| One-Scheduler lock | Concurrency | Implicitly | — | Keep |
| JSON contract | Interoperation | It is the access surface | Additive fields only | Keep |

"Demote" means it stops being part of the required path and becomes something used when needed.
Nothing is a clean delete.

### Where the maintainer's read holds

- **The log cannot carry a new pattern without a release.** Replay returns an error on any event type
  it does not know (`internal/state/state.go`, the `default` case of `Apply`), and every command replays
  the whole log. One unknown event makes every command on that log fail.
- **Agents never write to the log.** No command writes a Task, an edge, a claim, a heartbeat or a
  proposal. Each has one emitter, inside the Scheduler. An agent's only channel is one fenced block in
  its final output, which the Scheduler parses after the agent has exited. The golden rule that agents
  "extend the graph by emitting `TicketCreated`" describes the Scheduler doing it for them.
- **What one agent learns from another is a title and a commit id.** With a CLI backend the worker's
  summary is the Task title, and the context pack skips a summary that equals the title
  (`dependencyContext` in `internal/orchestrator/orchestrator.go`).
- **Dispatch replaces a session; it does not join one.** The Scheduler path needs a workspace, a
  config file, a separate `aoa run` and, for pull requests, a dedicated clone. The Merge Queue verifies
  by merging and resetting inside the repository's working tree, so it cannot share a checkout with a
  session someone is working in.
- **Three mechanisms are human workflow and say so:** the approval gate, pull request delivery and the
  sessions table.

### Where it does not hold

- **Most of the pipeline is concurrency control, not human process.** Work in isolation, offer a
  candidate, check it against the current shared state, commit or retry: that is an optimistic commit
  protocol. Human pull-request workflow has the same shape for the same reason. Removing the repository
  lock once lost a merged commit in four of five concurrent runs
  ([#131](https://github.com/bharadwaj6/ageOfAgents/issues/131)).
- **Goal-level verbs are already usable from a session.** Submit, amend, cancel, status, events and
  wait are shell commands with JSON output, idempotent and safe under concurrent writers
  ([the contract](../backend.md)).
- **The Gate is already the most accessible and extensible mechanism.** `aoa sessions check` runs it on
  any worktree with no workspace, and a check is any command that exits non-zero.
- **Backends, checks, the pull request opener, budgets and limits are already data.** The closed part
  is the log's meaning.
- **"0 of 20" is narrower than it reads.** The pilot's own conclusion is that precision cannot be
  estimated for those backends, with an interval reaching 0.28 per arm.

### One decision that went against the origin on purpose

The origin prompt says agents "can communicate much better" than people.
[ADR 006](adr/006-emergent-task-graph-blackboard.md) chose the opposite: agents coordinate through
shared state, not messages. Combined with a fresh process per attempt, the result is a narrower
hand-off than human colleagues get. That choice, more than the Gate, is why the system reads like
tickets.

### Three places the documents and the code disagree

- [ADR 003](adr/003-flat-orchestrator-worker.md) says idle workers pull Tasks. The Scheduler assigns
  them and writes the claim itself; nothing contends for a claim.
- Golden rule 5 says agents emit `TicketCreated`. They do not append to the log at all.
- `StateSnapshot` has a replay case and no emitter since compaction was removed. `TicketInvalidated`
  and `TicketAmended` are emitted only from result fields that no shipped backend sets.

### What could be reused unchanged

The append-only log and its check-then-append transaction (`internal/ledger`), the file lock
(`internal/filelock`), the lock-free resumable cursor, the log location in the git common directory
that needs no workspace, read-only worktree observation, the Gate as a plain command runner with a
verdict pinned to content, and idempotency keys. Not reusable as they stand: replay, which rejects
unknown events; `aoa events`, which requires a workspace and so cannot read the repository-level log;
and `aoa sessions`, which prints text only.
