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

## What the literature says about when coordination is needed

Each source below was fetched and read, and each figure was checked against the paper's text. Where a
line is an inference from a source and not the source's own statement, it says so.

### Theory

| Source | What it says | What it implies here (inference) |
|---|---|---|
| Hellerstein and Alvaro, *Keeping CALM* ([arXiv 1901.01930](https://arxiv.org/abs/1901.01930)) | "A program has a consistent, coordination-free distributed implementation if and only if it is monotonic." Insertions are monotonic; updates and deletions in general are not | Adding a file, a test or a new symbol needs no coordination. Renaming, deleting or changing the signature of something shared does |
| Bailis et al., *Coordination Avoidance in Database Systems* ([arXiv 1402.2237](https://arxiv.org/abs/1402.2237)) | "Coordination can only be avoided if all local commit decisions are globally valid." A foreign-key insert is safe, a delete is not; choosing a specific unique value is not, choosing some value is | The same split, with a test for it. Sequential numbering (migrations, versions) needs coordination; separate namespaces remove the need |
| Malone and Crowston, *The Interdisciplinary Study of Coordination* (ACM Computing Surveys, 1994) | "Coordination is managing dependencies"; with no interdependence there is nothing to coordinate. Dependency types: shared resources, prerequisite, transfer, usability, simultaneity, task and subtask | Different dependencies need different mechanisms. No single one, shared context or a gate, covers all of them |
| Gelernter, *Generative Communication in Linda* (ACM TOPLAS, 1985) | Three operations on one shared space: add, withdraw, read. Sender and receiver do not know each other and need not run at the same time | A small open store is enough to build claims, locks and notices, and it suits sessions that do not overlap in time |
| Erman et al., *The Hearsay-II Speech-Understanding System* (ACM Computing Surveys, 1980) | Independent modules "interact uniformly and anonymously via the blackboard". A scheduler still chose what ran next, and the authors say the flexibility "may no longer be needed" once a fixed method is known | Openness made interaction extensible. It did not remove the need to decide what runs |
| Mintzberg, *Structure in 5's* (Management Science, 1980) | Five coordinating mechanisms: direct supervision, standardization of work processes, of outputs, of skills, and mutual adjustment | A test gate standardizes outputs and leaves the process free. "Shared understanding" is mutual adjustment, which Mintzberg ties to small groups |

### Measurement

| Source | Finding |
|---|---|
| Uber, *Keeping Master Green at Scale* (EuroSys 2019) | With two concurrent changes to the same part of a repository there is a 5% chance they pass alone and fail together; with sixteen, 40%. Before the queue the mainline was green 52% of a week. A single serial queue does not scale, so the system verifies only combinations that can conflict |
| *Passes Alone, Fails Together* ([arXiv 2609.25396](https://arxiv.org/abs/2609.25396)) | On tasks built so that one patch changes an interface the other uses, the merge failed in 105 of 108 runs. On 417 real, reviewed pairs of Django pull requests it failed in 1 of 834 runs. A message of about 130 tokens describing the finished change recovered 82% to 98% |
| *CooperBench* ([arXiv 2601.13295](https://arxiv.org/abs/2601.13295)) | Two agents given coupled features succeed about half as often as one agent doing both. A message channel reduced merge conflicts and did not raise success. In 42% of reviewed failures, one agent had said what it was doing and the other acted as if it had not |
| Geng and Neubig, *Effective Strategies for Asynchronous Software Engineering Agents* ([arXiv 2603.21489](https://arxiv.org/abs/2603.21489)) | A manager with a dependency graph, one worktree per agent and a test-gated merge beat a single agent. A shared workspace with instructions was sometimes worse than one agent. Eight agents did worse than four. The serial, test-gated merge limited the speed-up, and the paper kept it |
| *ImpossibleBench* ([arXiv 2510.20270](https://arxiv.org/abs/2510.20270)) | When tests contradict the task, a strong model passed by cheating on 54% to 76% of tasks. Hiding the tests cut that to near zero. Read-only tests stopped test edits and not special-casing. A way to stop and flag the task cut one model from 54% to 9%. A retry loop raised cheating |
| *Are "Solved Issues" in SWE-bench Really Solved Correctly?* ([arXiv 2503.15223](https://arxiv.org/abs/2503.15223)) and *SWE-ABS* ([arXiv 2603.00520](https://arxiv.org/abs/2603.00520)) | An estimated 11% of test-passing patches are wrong in the first study; stronger tests reject 19.78% in the second |
| Cemri et al., *Why Do Multi-Agent LLM Systems Fail?* ([arXiv 2503.13657](https://arxiv.org/abs/2503.13657)) | Design and specification failures and misalignment between agents together outnumber verification failures about three to one. The largest single improvement the paper reports for a coding system still came from adding a verification step |

### What this supports and what it does not

- **It supports shared understanding as a real limit.** Failures on coupled work are about what the code
  should do, not where it goes, and a short accurate notice of a finished change prevents most of them.
- **It supports dropping a strictly serial queue.** Additive changes need no ordering. The systems that
  scaled verify only the combinations that can conflict.
- **It supports small shared primitives as a design.** Tuple spaces and blackboards are forty years old
  and were open in exactly the sense the maintainer asks for.
- **It does not support "not verification".** One in ten to one in five test-passing agent patches is
  wrong, agents weaken checks they can reach, and changes that pass alone do fail together where they
  overlap. Checking the combined result is what the theory requires for non-additive changes, whoever
  writes them.
- **It does not support "accessible primitives are enough".** Agents given a channel often did not use
  what it carried.
- **The cheapest coordination is choosing independent work.** On real, reviewed pairs of changes the
  measured interference was close to zero. If parallel sessions mostly touch unrelated things, neither a
  queue nor a shared log matters much.

Two documents could not be fetched and are not relied on: Nii's 1986 survey of blackboard systems, and
Graydon Hoare's post on the not-rocket-science rule, which the [reading list](reading-list.md) already
cites.

## What the tools already share between sessions

Read from each tool's own documentation, changelog or specification in November 2026. "Not stated"
means the document does not say.

| Tool | What sessions can share | Crosses vendors |
|---|---|---|
| Claude Code | A task list several sessions can open by name; agent teams with a task list and mailboxes, one team per session; plain-text messages between a person's sessions, for "a finding, a status, or a decision"; memory shared by every worktree of a repository on one machine | No, apart from instruction files and skills |
| Codex | Subagents returning summaries; direct messages between agents; an agent message board with read-only or read-write access, served over MCP since CLI 0.168.0; local memories, off by default | Not stated |
| Cursor | Projects with a store of files every member's agents read and write, a task board, and messages between coordinators. It loads Claude's and Codex's agent, skill and hook files | Configuration only |
| Symphony | Only the issue tracker's state. One orchestrator holds dispatch state, and each run is given its own issue and nothing else | The tracker is pluggable; the runner targets Codex |
| Beads | An issue graph with dependencies and an atomic claim, a ready queue, project memory injected at session start, messages, and `supersedes` links, through one CLI | Yes |
| Gas Town | Beads, plus mail that survives a session and nudges that do not. Its own design note says "Agents overuse mail" | Yes |
| StrongDM Attractor and CXDB | One context per pipeline run, with parallel branches isolated; a store of every conversation turn that wraps both `codex` and `claude` sessions | Yes |
| Plain files | `AGENTS.md` is read by Claude Code, Codex and Cursor; skills follow one open standard | Yes |

What none of the documents describes:

- **A claim or lease on files or paths.** Claims exist on tasks only. File conflicts are handled by
  isolation in worktrees, or by advice to partition the work.
- **A typed decision record.** Decisions travel as message text, notes, or repository files by
  convention.
- **Reading the live state of a session run by another vendor's tool.**

Two things the same documents say against building more shared state. Shared state that agents
maintain goes stale: Claude Code's own page says task status "can lag", and both Claude Code and Codex
call memory context, not enforcement. And OpenAI's account of building a product with agents
([Harness engineering](https://openai.com/index/harness-engineering/)) puts shared knowledge in the
repository, checked by linters and CI, and runs with "minimal blocking merge gates" because
"corrections are cheap, and waiting is expensive". It names its bottleneck as human QA capacity.

### The orchestration frameworks

The frameworks most often listed for building multi-agent systems put coordination inside one
program, and their shared state lasts one run or one thread. Read from each framework's own
documentation on 2026-11-16:

| Framework | How agents are coordinated | What state is shared, and how widely |
|---|---|---|
| LangGraph | A graph | Checkpoints "persist a thread's graph state", for "short-term, thread-scoped memory". A separate store is for "long-term, cross-thread memory", holding "application-defined data" |
| OpenAI Agents SDK | Handoffs, "represented as tools to the LLM"; by default the model chooses | "Handoffs stay within a single run" |
| Microsoft Agent Framework | Workflows with "explicit control over multi-agent execution paths" | "a robust state management system for long-running and human-in-the-loop scenarios" |
| Claude Agent SDK | The harness itself as a library: "the same tools, agent loop, and context management that power Claude Code" | Sessions to "resume or fork later"; hooks to "run custom code at key points in the agent lifecycle" |

What follows for `aoa`:

- **The Scheduler is the framework shape.** One control loop that owns the agents and their state is
  what these libraries provide and what the harnesses now run. Rebuilding it is not where `aoa` adds
  anything.
- **None of them crosses programs.** Shared state is reached by agents that one program started. Two
  sessions a person started separately, in different tools, are outside every row. That is the same gap
  the table above found.
- **LangGraph keeps two scopes apart.** Per-thread state is checkpointed; knowledge that outlives a
  thread goes in a store whose contents the application defines. The Event Log today is the first kind
  only, with a closed set of event types. Candidate 3 is the second kind.
- **Hooks are part of the agent loop.** The Claude Agent SDK lists hooks beside tools and sessions.
  That is where candidate 1 delivers its notice.
- **Delegation chosen by a model is native.** Handoffs are tools the model calls. Golden rule 3 keeps
  that out of `aoa`, and nothing here argues for adding it: the harness's lead agent already does it.

## How often this repository's own parallel work overlapped

Of the 139 merged pull requests not opened by a bot, 117 pairs were open at the same time, and 38 of
those pairs touched the same non-test Go file. That figure is an upper bound and a loose one: some
pairs are stacked pull requests, which are sequential by design, and after merging they cannot be told
apart from parallel work. The pairs known to be parallel are those involving a pull request `aoa`
delivered from a dogfood workspace: 18 pairs, of which 2 touched the same non-test Go file. Whether
either pair interfered is not recorded.

## Candidates

Hypotheses, not commitments. Each names what it closes, what already exists, the smallest form, what
it contradicts, and the signal that stops it. None is started.

### 1. An interference notice

- **What it closes.** The one case where the theory says coordination is required and where a short
  notice has a measured effect: one in-flight change removes, renames or re-signs something another
  in-flight change uses. Three of the 32 incidents were collisions between sessions. None of the three
  was an interface change.
- **Why the tools do not close it.** None describes a claim on files or a check across sessions.
  Worktree isolation defers the collision to the merge.
- **Smallest form.** `aoa sessions` already reads every linked worktree of a repository with no
  workspace and records the files each has changed. Add machine-readable output and an `overlaps` field
  listing the sessions that touch the same file. Detecting removed or renamed symbols comes later, and
  only if file overlap proves worth knowing.
- **How it reaches the agent.** Through a harness hook, not a tool the agent may choose to call. The
  one benchmark that tested a channel found agents often ignored what it carried. Claude Code and Codex
  both document a hook that runs before a tool call, Claude Code's can add text to the agent's context
  or block the call, and Cursor loads Claude Code's hook files. So the harness asks on the agent's
  behalf at the moment of an edit, and the same script serves three tools.
- **What it contradicts.** No golden rule. It does build on `aoa sessions`, which Part 4 of the
  [roadmap](roadmap.md) says to stop building on. That trial tested a table for a person to open; this
  is output for an agent to read.
- **Stop signal.** Measure before building. Record the maintainer's real parallel sessions for 14 days
  with the existing command. If fewer than three pairs of concurrent sessions touch a common file, stop:
  the sessions are independent, and choosing independent work was the coordination. Review no later than
  2026-12-14.

### 2. Environment facts as checks

- **What it closes.** The largest class of incident: 10 of 32, and two of the four Gate rejections.
- **Why the tools do not close it.** Memory stores a fact as prose and does not check it. A hook can
  run a script, but nothing supplies the checks.
- **Smallest form.** No new code in `aoa`. A checked-in list of commands that must pass on a machine
  set up correctly for this repository, run when a session starts. Only if that proves useful does
  `aoa doctor` learn to run it on any worktree without a workspace, using the Gate's runner, which
  already tells "could not run" from "failed".
- **What it contradicts.** Nothing.
- **Stop signal.** Four weeks. If the checks never fail, or sessions still fail on environment facts
  the checks could have covered, remove them.

### 3. Shared knowledge between sessions: adopt, do not build

- **What it closes.** Conventions, prior decisions and what another session did: 10 of 32 incidents.
- **What already exists.** Beads across vendors, the named task list and cross-session messages in
  Claude Code, a message board in Codex, and plain files in the repository.
- **Why not build it here.** `aoa` would be writing a weaker Beads. The measured risk is not that
  agents lack a channel but that they do not use what it carries.
- **Smallest form.** Pick one existing store and use it for two weeks. The only change in `aoa` worth
  considering is small and separate: replay tolerating event types it does not know, so the log stops
  breaking when extended. That amends golden rule 1 and needs a decision record.
- **Stop signal.** Read from agent behaviour, not from the maintainer's impression: the share of
  sessions that read from or wrote to the store without being told to. If it is under half after two
  weeks, a store is not the missing piece.

### What to stop

- New work on the Gate and the Merge Queue as the centre of the project. Both stay; neither is where
  the evidence points.
- Treating the Scheduler path as the required way in. It replaces a session, and the maintainer's
  sessions are where the work happens.
- The three disagreements between documents and code listed above should be corrected whatever
  direction is chosen.

If candidate 1's measurement comes back near zero, the honest conclusion is that a solo developer's
parallel sessions need no coordination layer beyond what the tools ship, and `aoa` is finished as a
small gated backend for those who want one.

## Where the evidence does not support the maintainer's read

- **"The Gate is not needed with full understanding."** The record shows the checks cost more than they
  caught here. It also shows wrong work passing weak or missing checks four times, and a fully
  instructed agent pushing to `main`. The literature puts wrong-but-passing agent patches at one in ten
  to one in five.
- **"Merge queues are decades old, so the framing is wrong."** Age is not the test. Changes that pass
  alone and fail together are measured, and verifying the combined result is what the theory requires
  for non-additive changes. What is wrong is making every change wait in one line.
- **"Too process-oriented."** In Mintzberg's terms a gate sets a standard for outputs and leaves the
  process free. Prescribing how agents communicate and plan is the process-heavy option.
- **"Neither accessible nor extensible."** True of the log and of everything below the Goal. Not true
  of the Gate, the backends or the Goal-level commands.
- **"Shared understanding is the limit."** Supported for coupled work. In this record the missing
  knowledge was mostly environment facts and conventions, a channel alone did not produce understanding
  in the one benchmark that tested it, and real parallel changes were rarely coupled.
- **"Harnesses do it natively."** They do, and that includes the shared-knowledge layer. Building one
  here would compete with the vendors and with Beads.

Not tested at all: that staying interactive is needed only when shared context is missing. That needs
session transcripts, and almost none survive for this repository.
