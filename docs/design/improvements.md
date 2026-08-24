# Improvement Roadmap — Quality & Agent Coordination

This is a **proposals** document: a prioritized set of bets to improve project quality and the
coordination between agents. It is not a record of decisions (those are [ADRs](adr/README.md)) nor the execution
tracker (that is [`roadmap.md`](roadmap.md)). Each item earns its place against a *measured failure mode*
or a concrete missing capability, and is checked against the [golden rules](https://github.com/bharadwaj6/ageOfAgents/blob/main/AGENTS.md) so nothing
here smuggles in an LLM coordinator, a second mutable store, or runtime voting.

## Framing: lean into the substrate, not into orchestration cleverness

Single-agent coding assistants (Codex, Claude Code) are strong *inside one session*: they read the
failing test output, recall what they just tried, and iterate. What they do **not** provide by default
is a durable, deterministic, *replayable* control plane across many isolated agents and retries: an
event-sourced log as the single source of truth, a verifier-gated linearizable merge queue, formal
invariants, and observability derived purely by replay (ADRs 001, 002, 012).

That substrate is `aoa`'s moat. The highest-value improvements are therefore the ones that make the
substrate do for a *fleet* what a single assistant does for one session — durably and deterministically.
Conversely, the things `aoa` should keep refusing are exactly the ones better models erode: debate,
voting, markets, role hierarchies, an LLM coordinator (ADRs 003, 005, 011). Every proposal below was
written to respect that line.

Ratings: **leverage** = expected impact on success/quality; **effort** = rough implementation cost.

---

## 1. Verification feedback loop — **shipped**

A human engineer fixes code by reading the build error and trying again; blind retries waste a whole
attempt. The Gate's combined output now flows through `VerificationFailed` → the ticket's
`LastFailOutput` → `agent.Task.{Attempt, LastFailure}` → the retry prompt, tail-bounded so a long build
log cannot blow up the token budget.

Codex and Claude Code get this for free inside one session, from live context. Doing it *across isolated
retries in separate worktrees* is the thing a fleet orchestrator has to build deliberately.

## 2. Richer shared-log context pack — **shipped (dependencies)**

ADR 006 says agents coordinate through shared state, which only means something if a worker can read it.
Each dispatch now carries a deterministic pack of the ticket's already-merged **dependencies** — title,
the worker's one-line summary, and the short merge commit — read from the Shared Log via
`dependencyContext` and injected by `BuildPrompt`. This delivers ADR 006 rather than deciding anything
new, so it carries no ADR of its own.

Deterministic assembly from replayed state, not semantic similarity: the same ticket produces the same
brief every time. Sibling and graph status were deliberately left out — merged dependencies are the
high-signal read, siblings are noise.

## 3. Flaky / nondeterministic-test detection — *the top real-world CI pain*

**Priority: P1 · Leverage: medium-high · Effort: medium**

**The need.** With `max_attempts` retries, a *flaky* Gate can pass on a lucky run and the proposal
merges — silently masking a real failure, or merging code that would fail under different conditions.
Flake quarantine is a perennial engineering pain that the current crash-loop heuristic (same-reason
failures) does not address: a test that flips between pass and fail is the opposite of a stable repeated
failure.

**Why Codex/Claude Code don't cover it by default.** They have no model of test nondeterminism at all.
`aoa` already runs the Gate as an objective, repeatable barrier and records every outcome on the log —
it is uniquely positioned to *detect* and *act on* flakiness.

**Mechanism.**
- *Detect (cheap, post-hoc):* a **replay projection** in `internal/diagnose` that flags a ticket/commit
  whose Gate both passed and failed. Buildable today from recorded `VerificationFailed` reasons, and far
  sharper once #1 lands the full `Output`.
- *Act (optional, opt-in):* a `confirm_runs` config that re-runs the Gate N times in
  `internal/mergequeue` before a merge stands, so a single lucky pass cannot merge. Default 1 (current
  behavior) to keep cost unchanged unless asked.

**ADR fit.** Quality through the objective Gate (ADR 002) plus observability as a replay projection
(ADR 012). No new control loop.

---

## 4. Blind-spot closure & post-merge safety — *act on the system's own #1 stated risk*

**Priority: P2 · Leverage: medium · Effort: medium-large**

**The need.** `metrics.md` names the verification blind spot — "an agent can make the Gate green while
silently breaking something the Gate does not cover" — as *the* core risk. The system **measures** it
(`regression_escape_rate` via the optional Shadow set) but never acts: there is no coverage gate and no
recovery path when a merge that passed the Gate later proves wrong.

**Why Codex/Claude Code don't cover it by default.** Coverage-aware gating across many automated merges,
and *replay/linearizable-history-driven bisection*, are control-plane capabilities. Because `aoa`
serializes merges (ADR 002) and stores them on an append-only log (ADR 001), bisecting "which merge
introduced the break" is nearly free — a property a single-session assistant simply does not have.

**Mechanism.**
- *Close the blind spot:* an optional coverage-delta command in the Gate config — fail a proposal whose
  new code is uncovered. Reuses the existing `verify` plumbing; no new concept.
- *Recover:* `aoa revert <commit>` plus replay-driven bisect over the linearizable merge history,
  triggered by a post-merge signal (e.g. a Shadow/regression failure or an operator).

**ADR fit.** Extends the Gate (ADR 002) and the event log (ADR 001). The revert/recovery path introduces
a new lifecycle concern and should land with its own ADR.

---

## Also on the radar (known / already roadmapped — listed for completeness, not core bets)

- **Dollar circuit-breaker & cost dashboard.** A token governor (`max_tokens_per_goal`) and a `$` ceiling
  (`max_usd_per_goal`) exist, and both now count *every* attempt including failed ones; a true cross-run
  dollar circuit breaker and a real-time burn view are still open (see *Not yet scheduled* below). Cost
  data is already event-sourced (`tokens_by_model`, `metrics.USD`).
- **Heartbeat-based stall detection.** *Shipped* — workers emit `Heartbeat` on
  `Options.HeartbeatInterval` (30s) for the duration of every Backend call, so the Stall Detector
  distinguishes a slow agent from a dead one instead of inferring liveness from the dispatch timestamp.
- **Multi-repo coordination & Firecracker sandboxing.** Captured in [`cross_repo.md`](cross_repo.md)
  and under *Not yet scheduled* below; not re-argued here.

## Explicit non-goals (kept here so the recommendations stay credible)

None of the above reaches for the machinery the design deliberately rejects. We continue to **not** add:
- multi-agent voting / debate / markets as a runtime control plane or selector (ADRs 005, 011);
- an LLM coordinator or a role hierarchy — there is exactly one deterministic scheduler (ADR 003);
- a second mutable store — all state remains derived by replaying the event log (ADR 001).

## Suggested sequencing

1. **#1 Verification feedback loop** first — highest leverage, smallest change, and it produces the
   `Output` data that sharpens both #2 and #3.
2. **#2 Context pack** and **#3 flaky detection** next; both reuse the projection pattern and the data #1
   adds.
3. **#4 blind-spot/post-merge safety** last — largest surface, needs its own ADR.

## Not yet scheduled

Moved here from the README, which now points at the docs rather than carrying a roadmap. These are
directions, not commitments — nothing below has a date, and several may never be built.

1. **Firecracker microVM sandboxing.** Docker isolates the Gate today. Firecracker would give stronger
   multi-tenant isolation at lower overhead, and could cover the *agent* as well as the Gate — which is
   the gap [`SECURITY.md`](https://github.com/bharadwaj6/ageOfAgents/blob/main/SECURITY.md) is candid
   about.
2. **Persistent server mode.** `aoa serve` handles GitHub webhooks. A durable server would add a
   dashboard over the Event Log, live task graphs, and remote control of a running orchestrator.
3. **A cross-run `$` circuit breaker.** `max_usd_per_goal` bounds one goal. Nothing bounds a week of
   them.
4. **Cross-repo dependency management.** One workspace is one repository. Atomic merges spanning several
   is designed in [`cross_repo.md`](cross_repo.md) and unimplemented.

Speculative merge, best-of-N with the suite as verifier, and SPRT early-stopping are tracked with
explicit reopen gates in [the roadmap](roadmap.md) instead.

### Why log compaction is not on this list

It shipped, then was **removed rather than fixed**. Compaction rewrote the log to a single snapshot,
which `metrics`, `diagnose`, `otel` and the invariant checker all read as zeros — and because a snapshot
carries no attempt history, that is not fixable. A compacted log and replay-derived metrics are mutually
exclusive; replay won. Recorded here so it is not proposed again.
