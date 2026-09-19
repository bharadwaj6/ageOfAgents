# Roadmap

What is settled, what is open, and the one thing that would most change the project's standing.

For what has already shipped, read [`CHANGELOG.md`](https://github.com/bharadwaj6/ageOfAgents/blob/main/CHANGELOG.md).
For the reasoning behind individual decisions, read the [decision records](adr/README.md).

## Settled

These are not open questions, and proposals to revisit them should expect to argue against a decision
record rather than a preference:

- The Event Log is the single source of truth; all state is a replay of it ([ADR 001](adr/001-event-sourced-truth.md)).
- Nothing merges that the Gate has not passed, on the **post-merge** state ([ADR 002](adr/002-verifier-gated-merge-queue.md)).
- One deterministic Scheduler, no LLM in the control plane ([ADR 003](adr/003-flat-orchestrator-worker.md)).
- No markets, no voting, no debate as a live control plane ([ADR 005](adr/005-no-markets-no-consensus.md),
  [ADR 011](adr/011-debate-markets-as-offline-tools.md)).
- Agents coordinate through the shared log, not messages ([ADR 006](adr/006-emergent-task-graph-blackboard.md)).
- Observability is a replay projection, never hot-path instrumentation ([ADR 012](adr/012-observability-as-replay-projection.md)).
- `aoa` is a backend: front doors decide what gets worked on, `aoa` decides whether it lands ([ADR 015](adr/015-aoa-is-a-backend.md)).

## The open question that matters

**Does the Gate change outcomes at scale?** This is the project's central claim and it is unproven.

Every SWE-bench figure recorded so far was produced with the Gate *disabled*, so those runs measure the
backend agent rather than the merge queue. The first Gate-on/Gate-off comparison covered two instances —
enough to show the mechanism works, not enough to support a rate. The harness is built and the
methodology is written down; what is missing is budget and a run at scale.

Everything else on this page is secondary to that. See [live evaluation](live_eval.md) for the numbers
and their caveats, and [metrics](metrics.md) for what would count as an answer.

## Direction: the gated backend

[ADR 015](adr/015-aoa-is-a-backend.md) positions `aoa` as the execution backend that end-to-end
automators drive. The automators are firstmate, Symphony-style board runners, Linear agents, CI jobs
and people. They decide *what* gets worked on; `aoa` decides *whether it lands*. This section is the
living tracker for that work. Each PR ticks its own line.

### Resuming agent: start here

Take the first unticked item below.

- Its dependencies are in the right-hand column. Branch from `main`, or stack on the dependency's
  branch if that hasn't merged yet.
- Each item is red-test-first and runs `make check` (read the output).
- Each item is one PR, rebase-merged.
- The design detail for each item is in [ADR 015](adr/015-aoa-is-a-backend.md) §5.

When your PR merges, tick its line here and update **Last status**.

**Last status (2026-09-19):** v0.4.0 is released. On `main`: ADR 016 (#147), PR delivery (#148, #149)
and the GitHub Issues front door. Next is self-hosting on the maintainer's Mac, then the first dogfood
issues, then v0.5.0.

| | Increment | Depends on |
|---|---|---|
| [x] | Doc fixes: `events` flag examples ([#127](https://github.com/bharadwaj6/ageOfAgents/pull/127)) | — |
| [x] | ADR 015, tracker and positioning ([#133](https://github.com/bharadwaj6/ageOfAgents/pull/133)) | — |
| [x] | `serve` queues work only from trusted commenters, `--allow` ([#134](https://github.com/bharadwaj6/ageOfAgents/pull/134)) | — |
| [x] | Event Log safe for writers in several processes, `internal/filelock` ([#136](https://github.com/bharadwaj6/ageOfAgents/pull/136)) | — |
| [x] | One Scheduler per workspace, `aoa run` exits `75` when busy ([#137](https://github.com/bharadwaj6/ageOfAgents/pull/137)) | ledger lock |
| [x] | Write verbs: goal `--source/--ref/--key`, idempotent submit, `--json` on goal/amend/approve/reject ([#138](https://github.com/bharadwaj6/ageOfAgents/pull/138)) | ledger lock |
| [x] | `status --json` from one projection shared with text `status` ([#140](https://github.com/bharadwaj6/ageOfAgents/pull/140)) | write verbs |
| [x] | Event cursor: `events --json --since N` and `--follow` ([#139](https://github.com/bharadwaj6/ageOfAgents/pull/139)) | ledger lock |
| [x] | Cancellation: `GoalCancelled`, `aoa cancel`, invariant `CancelHonored` ([#141](https://github.com/bharadwaj6/ageOfAgents/pull/141)) | write verbs, status |
| [x] | Contract reference `docs/backend.md` and README pointer ([#142](https://github.com/bharadwaj6/ageOfAgents/pull/142)) | all of the above |
| [x] | A stopped Docker daemon is infrastructure, not a Gate verdict ([#144](https://github.com/bharadwaj6/ageOfAgents/pull/144), fixes #135) | — |
| [x] | Release [v0.4.0](https://github.com/bharadwaj6/ageOfAgents/releases/tag/v0.4.0) | all of the above |

**Part 2: `aoa` builds `aoa`.** A GitHub Issue labelled `aoa` becomes a goal, and `aoa` opens a PR. It runs
on the maintainer's Mac on a subscription backend.

| | Increment | Depends on |
|---|---|---|
| [x] | [ADR 016](adr/016-deliver-a-goal-as-a-pull-request.md): deliver a Goal as a pull request ([#128](https://github.com/bharadwaj6/ageOfAgents/issues/128)) | v0.4.0 |
| [x] | `Delivered` / `DeliveryFailed` events and the `delivered` outcome, replay only ([#148](https://github.com/bharadwaj6/ageOfAgents/pull/148)) | ADR 016 |
| [x] | `[delivery] mode = "pr"`: one PR per goal from a Gate-verified `aoa/<goal>` branch ([#149](https://github.com/bharadwaj6/ageOfAgents/pull/149)) | replay |
| [x] | GitHub Issues front door + contract-conformance test, `examples/github-issues` ([#130](https://github.com/bharadwaj6/ageOfAgents/issues/130)) | PR mode |
| [ ] | Self-hosting on the maintainer's Mac: dedicated clone, launchd, stop switches | front door |
| [ ] | First dogfood PRs: a docs micro-issue, [#146](https://github.com/bharadwaj6/ageOfAgents/issues/146), [#143](https://github.com/bharadwaj6/ageOfAgents/issues/143), [#132](https://github.com/bharadwaj6/ageOfAgents/issues/132) | self-hosting |
| [ ] | Release v0.5.0 | all of the above |

**Later increments** each need a design decision, and most need an ADR, before any code:

- reporting back to the origin ([#129](https://github.com/bharadwaj6/ageOfAgents/issues/129));
- a team HTTP transport for the same verbs, with auth and several workspaces
  ([#76](https://github.com/bharadwaj6/ageOfAgents/issues/76)), plus a cross-run budget
  ([#77](https://github.com/bharadwaj6/ageOfAgents/issues/77));
- reference integrations for firstmate, Linear and Symphony
  ([#130](https://github.com/bharadwaj6/ageOfAgents/issues/130); GitHub Issues is Part 2);
- one repo adopted by two workspaces ([#131](https://github.com/bharadwaj6/ageOfAgents/issues/131)).

## Not yet scheduled

Directions, not commitments. Detail and rationale in [proposals](improvements.md#not-yet-scheduled):

- **Firecracker microVM sandboxing** — Docker isolates the Gate; the agent itself is not confined.
- **Persistent server mode.** A durable server with a dashboard over the Event Log, now framed as the
  team transport for the backend contract ([#76](https://github.com/bharadwaj6/ageOfAgents/issues/76)).
- **A cross-run `$` circuit breaker** — `max_usd_per_goal` bounds one goal; nothing bounds a week.
- **Cross-repo dependency management** — designed in [cross-repo](cross_repo.md), unimplemented.

## Deliberately deferred, with reopen conditions

These were considered and set aside. Each names the evidence that would justify revisiting it, so the
decision is falsifiable rather than permanent:

| Deferred | Reopen when |
|---|---|
| Speculative / batched merge with an adaptive window | `merge_queue_wait_mean` climbs while queue depth stays high — i.e. serialization is demonstrably the bottleneck |
| Best-of-N generation with the test suite as selector | Per-task cost data shows the extra attempts are cheaper than the retries they replace |
| SPRT early-stopping for live evals | Eval runs get large enough that fixed-N sampling is the dominant cost |

Autonomous work discovery has left this table. It is no longer deferred: [ADR 015](adr/015-aoa-is-a-backend.md)
assigns discovery to the front door, so `aoa` will not build it.

## Not coming back

**Log compaction.** It shipped and was removed rather than fixed: it rewrote the log to a single
snapshot, which `metrics`, `diagnose`, `otel` and the invariant checker all read as zeros. A snapshot
carries no attempt history, so this is not fixable — a compacted log and replay-derived metrics are
mutually exclusive. Replay won.
