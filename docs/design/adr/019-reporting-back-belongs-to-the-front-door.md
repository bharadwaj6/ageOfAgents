# ADR 019: Reporting Back Belongs to the Front Door

## Status
Accepted. Extends [ADR 015](015-aoa-is-a-backend.md) (where the boundary runs) and bounds
[ADR 012](012-observability-as-replay-projection.md) (what a replay projection is for). Resolves
[#129](https://github.com/bharadwaj6/ageOfAgents/issues/129).

Bounded in turn by [ADR 020](020-task-lifecycle-is-a-projection.md), which publishes the Goal
lifecycle a caller reads. This record governs what `aoa` will not **send**; ADR 020 governs what a
caller may **ask**. A read projection needs neither the credentials nor the cursor this record found
nowhere to put, so the two stand together rather than in tension.

## Context
`aoa` never speaks to anyone. Work arrives, the Gate decides, the Event Log records it — and whoever
asked hears nothing unless they come and look. Someone who commented `@aoa fix the flaky test` on an
issue is never told that it merged, failed, or is parked for approval.

The proposal on the table was to close that with an **outbound notification sink shaped like
`internal/otel`**: opt-in config, a projection of the log, never in the control loop, turning `Merged`,
`TicketFailed`, `ApprovalRequested` and friends into a command or an HTTP POST, addressed with the
Goal's `ref`.

The shape is right and the projection discipline is real, but the analogy does not carry. OTel export is
stateless and addressee-free: the operator sets one endpoint in the environment, delivery is
at-most-once, a dropped span costs nothing, and no reply ever comes back. Reporting back is the
opposite on every axis. It is **at-least-once delivery to an addressee derived from the content** (this
Goal's issue, that Goal's Linear ticket), it needs **credentials per destination**, and it needs a
**durable cursor** so that a restart neither re-reports nor skips.

That cursor is the load-bearing problem, and inside `aoa` it has nowhere to live:

- A file beside the log is a side table, which [ADR 001](001-event-sourced-truth.md) refuses.
- An event is worse. It would make a projection a writer of the log it projects, and it would put one
  external system's delivery bookkeeping into the audit trail that replay, the budgets and the Gate's
  history are read from. A `NotificationSent` event is not a fact about the work; it is a fact about
  somebody's Slack.

Outside `aoa` the same cursor is trivial, because the caller already holds one: the backend contract
guarantees `events --json --since N [--follow]` as a resumable stream, and `status --json` as the
snapshot it resumes against. `examples/github-issues` already reports every outcome back to its issue
using nothing of `aoa`'s but those two verbs, and its conformance test runs in `make check`.

## Decision
**`aoa` emits no outbound notifications. Reporting back is the front door's, done over the backend
contract.** [ADR 015](015-aoa-is-a-backend.md) already gives the front door the conversation with the
requester; this is that clause applied to the return leg.

The supported recipe, documented in [driving aoa](../../backend.md#reporting-back):

1. **What happened** — `aoa events --json --since <cursor> [--follow]`. The reportable events are
   `Merged`, `TicketFailed`, `ApprovalRequested`, `GoalBudgetExceeded`, `GoalCancelled`, `Delivered`
   and `DeliveryFailed`.
2. **Who to tell, and what to say** — `aoa status --json`. An event names a task; the snapshot joins
   that task to its Goal, the Goal to the `ref` its submitter recorded, and the Goal to the outcome
   worth reporting. Goal-level outcome is a fold, so it belongs to the snapshot, not to any one event.
3. **The cursor is the reporter's**, and the best one is the report itself. The reference front door
   reads back the markers in its own comments, so at-least-once delivery becomes exactly-once *visible*
   reporting with no state of its own to lose.
4. **Loop avoidance stays where intake is.** A report that could re-trigger intake is only a hazard for
   whoever owns both ends; the front door owns both. `aoa serve`'s `--allow` and the reference front
   door's label trigger already bound it. Were `aoa` to post its own comments, its own `@aoa` intake
   would have to learn to ignore itself.
5. **`aoa serve` stays intake-only.** It queues work from an `@aoa` comment and says nothing back. A
   GitHub loop that reports is `examples/github-issues`.

Two outbound things `aoa` already does are not notifications. Opening a pull request is **delivery of
the work itself** (ADR 016) — the artefact, to the forge named in the config. Exporting OTLP is
**telemetry** (ADR 012) — to an endpoint the operator set in the environment. Neither is addressed at a
person the log names, neither carries a message about someone's request, and neither needs a cursor,
which is precisely why they fit in a projection and a notifier does not.

## Consequences
- **#129 closes with no code**: no new config surface, no credentials in `aoa.toml`, no retry/backoff
  machinery, and no config-driven subprocess or HTTP call — all of which would be a delivery system
  rather than a projection.
- **Nothing in ADR 012 is relaxed or extended.** It stays the shape for *observability*: telemetry to an
  endpoint the operator configured. It is not a precedent for delivering messages to addressees the log
  names.
- **Tradeoff: someone running only `aoa serve` still gets no reply on their issue.** The answer is a
  front door — the reference one is a single bash script that polls, reports once per outcome and needs
  no public endpoint.
- **Tradeoff: every front door re-solves the cursor and the idempotent report.** That is ~40 lines of
  bash today. If a third or fourth reference integration repeats it, the shared piece belongs in
  `examples/`, not in the core.
- **Reopen condition.** The team HTTP transport ([#76](https://github.com/bharadwaj6/ageOfAgents/issues/76))
  is a long-running process with durable state of its own. If it lands, the "the cursor has nowhere to
  live" argument no longer holds, and outbound delivery should be reconsidered **there** — in the
  server, still outside the Scheduler — rather than in the CLI.
- The recipe is pinned by `TestReportingBackNeedsOnlyTheContract` in `cmd/aoa`, so a contract change
  that would break a reporting front door fails the build rather than someone's cron job.

## Research basis
Engineering judgement about where a system boundary runs, resting on evidence already in this
repository rather than on a paper: the reference front door reports back over the contract today, with
no `aoa` code, and `make check` proves it.
