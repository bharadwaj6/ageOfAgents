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

## The open question that matters

**Does the Gate change outcomes at scale?** This is the project's central claim and it is unproven.

Every SWE-bench figure recorded so far was produced with the Gate *disabled*, so those runs measure the
backend agent rather than the merge queue. The first Gate-on/Gate-off comparison covered two instances —
enough to show the mechanism works, not enough to support a rate. The harness is built and the
methodology is written down; what is missing is budget and a run at scale.

Everything else on this page is secondary to that. See [live evaluation](live_eval.md) for the numbers
and their caveats, and [metrics](metrics.md) for what would count as an answer.

## Not yet scheduled

Directions, not commitments. Detail and rationale in [proposals](improvements.md#not-yet-scheduled):

- **Firecracker microVM sandboxing** — Docker isolates the Gate; the agent itself is not confined.
- **Persistent server mode** — a durable server with a dashboard over the Event Log.
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
| Autonomous work discovery (beyond the webhook path) | The hardened webhook path proves insufficient on a real repository |

## Not coming back

**Log compaction.** It shipped and was removed rather than fixed: it rewrote the log to a single
snapshot, which `metrics`, `diagnose`, `otel` and the invariant checker all read as zeros. A snapshot
carries no attempt history, so this is not fixable — a compacted log and replay-derived metrics are
mutually exclusive. Replay won.
