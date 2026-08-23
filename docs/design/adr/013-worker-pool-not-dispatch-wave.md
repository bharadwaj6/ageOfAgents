# ADR 013: Dispatch Is a Worker Pool, Not a Wave

## Status
Accepted

## Context
`ReconcileOnce` dispatched work as a **wave**: it launched up to `Concurrency` goroutines, blocked on
`wg.Wait()`, and only then drained the merge queue. That barrier is the whole problem.

- **`Concurrency` was a wave size, not a steady-state pool.** `slots := Concurrency - ActiveCount()` was
  computed once per pass, so a worker that finished early left its slot idle until every sibling in the
  wave finished.
- **The slowest agent in a wave blocked every merge in it.** A proposal ready at t=1min waited for the
  wave's straggler before the merge queue looked at it — bounded only by `AgentTimeout` (30m default).
  With `BestOfN > 1` the parallel attempts on a single ticket join the same wave and amplify this.
- **The Stall Detector could never fire on a stuck worker**, because it runs *after* the barrier. It only
  ever observed workers that had already returned. (`AgentTimeout`, added separately, means this is no
  longer an unbounded hang — but the detector remains structurally unable to see a live worker.)

The barrier was never a correctness requirement. It was the simplest way to write the first version, and
the merge queue's serialization — the property that actually matters (ADR 002) — comes from the queue
being drained by a single caller, not from workers being quiesced.

## Decision
Dispatch becomes **asynchronous across reconcile passes**, and the pass no longer waits for it.

- `ReconcileOnce` launches dispatch goroutines on an orchestrator-owned `sync.WaitGroup` and returns
  without joining them. Each pass tops the pool up to `Concurrency` using `ActiveCount()`, which is
  already derived from the Event Log (`TicketClaimed`/`WorkStarted` minus terminal events) and is
  therefore the authoritative count of in-flight attempts.
- The merge queue is drained **every pass**, so a finished proposal is verified on the next pass rather
  than waiting for its slowest sibling.
- The merge queue itself is unchanged and remains the single writer to `main`. Only `Run` drains it, one
  proposal (or one disjoint batch) at a time. **ADR 002 is untouched.**
- `Run` gains two obligations that the barrier previously provided for free:
  1. A pass that makes no ledger progress **while workers are in flight** is not a stall — it sleeps for
     `PollInterval` and continues, instead of failing the run.
  2. `Run` waits for all dispatch goroutines before returning, so no worker outlives the call. The wait
     also runs on the error paths, so a failing run cannot leak workers into a caller's process.

## Consequences
- Throughput: a free slot is refilled on the next pass rather than at the end of a wave, and merge latency
  is bounded by the poll interval rather than by the slowest agent in a batch.
- The Stall Detector now observes genuinely in-flight workers, which is what it was written for.
- `aoa run` remains a single deterministic control loop with no second controller (ADR 003). What changed
  is when the loop joins its workers, not who decides what runs.
- The event log is unchanged: no new event types, and every state transition is still derived by replay
  (ADR 001). A crash mid-pass is recovered exactly as before, because in-flight work was never durable —
  a claimed ticket with no terminal event is already the recovery signal.
- Cost: `Run` now polls while workers are busy. `PollInterval` (default 100ms, injectable) trades
  responsiveness against wakeups; the loop is otherwise idle, and `Sleep` is already injected in tests so
  the suite does not pay real time for it.
- Tradeoff considered and rejected: draining the merge queue from its own goroutine, concurrently with
  dispatch. It removes the poll but puts a second long-lived loop inside the orchestrator, which is
  exactly the shape ADR 003 exists to prevent. Polling in the one loop is the boring option.

## Research basis
Standard bounded worker-pool practice; the specific failure mode (a synchronous barrier converting a
per-item latency bound into a per-batch one) is head-of-line blocking. The serialization requirement it
must not break is ADR 002; the single-controller requirement is ADR 003.
