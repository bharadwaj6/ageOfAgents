# Improvement Roadmap — Quality & Agent Coordination

This is a **proposals** document: a prioritized set of bets to improve project quality and the
coordination between agents. It is not a record of decisions (those are [ADRs](adr/)) nor the execution
tracker (that is [`roadmap.md`](roadmap.md)). Each item earns its place against a *measured failure mode*
or a concrete missing capability, and is checked against the [golden rules](../../AGENTS.md) so nothing
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

## 1. Verification feedback loop — *the failing-build iteration every engineer relies on*

**Priority: P0 · Leverage: high · Effort: small · Status: shipped**

> Implemented: the Gate's combined output now flows through `VerificationFailed` → ticket
> `LastFailOutput` → `agent.Task.{Attempt,LastFailure}` → the retry prompt (tail-bounded). The
> design rationale below is retained for context.

**The need.** A human engineer fixes code by reading the build/test error and trying again. `aoa`
currently throws that signal away. When a proposal fails the Gate, `rejectOrFail`
(`internal/orchestrator/orchestrator.go`) emits `VerificationFailed` with only a short `Reason`; the
verifier's full combined output — captured in `verify.Result.Output` and carried out of the merge queue
as `Outcome.Output` — is **not** persisted into the event (the `Output` field on
`api.VerificationFailedPayload` exists but is left unset). The worktree is then cleaned up, and the next
attempt runs an **identical** prompt (`agent.BuildPrompt`): `agent.Task` has no field for prior-attempt
failure. Retries are blind.

**Why Codex/Claude Code don't cover it by default.** They keep error output in the live context of a
single session. `aoa`'s differentiated value is doing the same thing *across isolated retries and
distinct workers, durably, by replaying the log* — so a fresh process (or a different worker) picks up
exactly where the last attempt failed. That is a control-plane capability, not a prompt trick.

**Mechanism (all event-sourced; no new control loop, no LLM in coordination).**
1. Plumb `Outcome.Output` through `applyMergeOutcome` → `rejectOrFail` into
   `VerificationFailedPayload.Output` (the field is already defined in `pkg/api/events.go`).
2. Project the most-recent failure onto the ticket in `internal/state/state.go`, next to the existing
   `LastFailReason` / `SameFailCount`: add a (truncated) `LastFailOutput`.
3. Add `Attempt int` and `LastFailure string` to `agent.Task` (`internal/agent/agent.go`), populated at
   dispatch from ticket state.
4. Extend `agent.BuildPrompt` to surface it: *"Attempt N. Your previous attempt failed the Gate with the
   following output: <truncated output>. Fix the cause; keep the change minimal."*

**ADR fit.** Pure projection of the event log (ADR 001), strengthening the Gate-driven loop (ADR 002),
delivered through the single `agent.Backend` seam (ADR 004). Nothing here is stochastic coordination.

**Tension to settle in design.** Retries currently `cleanupWorktree` (fresh checkout each time).
Forwarding the *failing diff* as well as the output is possible but adds state and ambiguity;
recommend **output-first** (simplest, YAGNI) and revisit only if measurement shows agents repeatedly
re-deriving the same wrong diff.

**Measurement.** Expect `mean_attempts_to_merge` and `retry_churn` (already computed in
`internal/metrics` / `internal/diagnose`) to fall once the loop is informed.

---

## 2. Richer shared-log context pack — *realize the blackboard the design already promises*

**Priority: P1 · Leverage: high · Effort: medium · Status: shipped (dependencies)**

> Implemented (first slice): each dispatch now carries a deterministic context pack of the ticket's
> already-merged **dependencies** — title, the worker's one-line `Summary`, and the short merge commit —
> read from the Shared Log via `dependencyContext` and injected by `BuildPrompt`. This required plumbing
> the previously-dropped `Result.Summary` through `ProposalSubmitted` onto ticket state. No new ADR: it
> *delivers* ADR 006 rather than deciding anything new. Sibling/graph status was deliberately left out
> (YAGNI — merged dependencies are the high-signal blackboard read; siblings add noise). The rationale
> below is retained for context.

**The need.** Before touching code, an engineer reads the surrounding code, the tickets it depends on,
and what already landed. ADR 006 says agents "coordinate through shared state, not messaging" — but today
an agent receives only five `Task` fields (`TicketID`, `Title`, `Goal`, `Worktree`, `Conventions`) and
has no structured view of the blackboard it is supposed to read. The shared log is the coordination
medium, yet workers cannot actually read it.

**Why Codex/Claude Code don't cover it by default.** A per-task brief, assembled **deterministically by
replaying a shared coordination log** across many parallel agents, is fundamentally different from one
assistant's chat history. It is the blackboard model (ADR 006) made real.

**Mechanism.** A deterministic, `state`-derived *context pack* built at dispatch and injected into the
prompt — no messaging, no new store:
- Summaries/traces of this ticket's **dependencies** (already on the log as `ProposalSubmitted.Summary`
  and the reasoning `trace`).
- This ticket's **prior failed approaches** (overlaps with #1; share the projection).
- Relevant **sibling/graph status** so the agent understands where its task sits.

Keep it bounded and deterministic; the pack is a pure function of the event log, mirroring the projection
pattern in `internal/metrics` / `internal/diagnose`.

**ADR fit.** Delivers ADR 006's promise without violating ADR 003 (still one deterministic scheduler) or
ADR 001 (still derived from the log). Likely warrants a short ADR documenting the context-pack contract.

**Note.** Watch token cost (context size scales with graph fan-in); cap the pack and prefer summaries
over raw diffs. This is the same scaling concern flagged for cross-repo work in `cross_repo.md`.

---

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
  dollar circuit breaker and a real-time burn view are on the README roadmap. Cost data is already
  event-sourced (`tokens_by_model`, `metrics.USD`).
- **Heartbeat-based stall detection.** *Shipped* — workers emit `Heartbeat` on
  `Options.HeartbeatInterval` (30s) for the duration of every Backend call, so the Stall Detector
  distinguishes a slow agent from a dead one instead of inferring liveness from the dispatch timestamp.
- **Multi-repo coordination & Firecracker sandboxing.** Already captured in `cross_repo.md` and the
  README roadmap; not re-argued here.

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
