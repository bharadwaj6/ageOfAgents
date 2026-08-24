# `aoa` and loop engineering

"Loop engineering" is the name that settled, in mid-2026, on a shift the industry had been circling for a
while: the practitioner stops prompting an agent turn by turn and instead builds the system that
**discovers** work, **hands it off** in isolation, **verifies** the result, **persists** state, and
**schedules** the next cycle. The framing is Addy Osmani's; the same five moves show up independently in
Peter Steinberger's OpenClaw, Geoffrey Huntley's "Ralph" technique, and Stripe's internal Minions fleet.

This document scores `aoa` against that model, and — the part worth being explicit about — states which
of the model's recommendations `aoa` **refuses**, and why each refusal is a design position rather than
an omission.

Short version: **`aoa` is not a system that should adopt loop engineering. It is a loop**, built to a
stricter standard than the model asks for on the three moves that are hard: handoff, verification and
persistence.

## The five moves

| Move | `aoa` | Where |
|------|-------|-------|
| **Discovery** — surface work without being asked | **partial** | Work enters via `aoa goal` or an `@aoa` issue comment (`cmd/aoa/serve.go`). Nothing polls CI, triages issues, or scans commits. See [the open gap](#the-one-real-gap-discovery). |
| **Handoff** — isolate parallel agents | **ahead of the model** | Per-attempt git worktrees cut from `HEAD` with random branch suffixes (`internal/worktree`), *plus* a serializing merge queue with post-merge verification and `ResetHard` rollback (`internal/mergequeue`). The model asks for worktrees so agents don't collide; `aoa` also guarantees `main` stays linearizable and green (ADR 002). |
| **Verification** — external backpressure, not self-grading | **ahead of the model** | The Gate is `go build` / `go test` / your commands, run on the **post-merge** state, never on the candidate in isolation. A `regression_verify` Shadow set measures what the Gate itself misses, without blocking (`regression_escape_rate`). |
| **Persistence** — survive the context window | **ahead of the model** | An append-only JSONL Event Log is the single source of truth; all state is a pure fold (ADR 001), with torn-write repair and crash recovery. Retry prompts carry the prior Gate output; dispatches carry a context pack of merged dependencies (ADR 006). |
| **Scheduling** — decouple from the operator | **solved without a daemon** | `aoa run` is idempotent, crash-recoverable, and quiet when idle, so cron / systemd / Actions *is* the scheduler. Recipes in [`../scheduling.md`](../scheduling.md); `--interval` for interactive use. |

### On verification, specifically

The research that motivates loop engineering spends a long section establishing that LLMs cannot
*intrinsically* self-correct — that asking a model to review its own reasoning without external grounding
degrades accuracy as often as it improves it, and that this is a property of the architecture, not a
training gap. It then prescribes an **adversarial maker–checker sub-agent**: a second LLM, fresh context,
told to assume the output is broken.

That prescription concedes most of the argument and then stops one step short. If a model cannot reliably
judge correctness, a second instance of that model is a weaker oracle than a compiler. `aoa`'s answer is
the one the evidence actually points at: **the checker is the build and the test suite**, and the thing it
checks is the post-merge state of `main`, not the diff in isolation. No LLM sits in the verification path,
so no amount of self-persuasion in a context window can move the merge decision.

This was settled before the vocabulary existed — ADR 002 (verifier-gated merge queue), ADR 005 (no
consensus or voting), ADR 011 (debate rejected as a live control plane, permitted offline). The
loop-engineering literature is, on this point, an independent argument for a decision already recorded
here.

## The four debts

The model names four debts an unattended loop accrues. They are a good checklist:

| Debt | `aoa` |
|------|-------|
| **Verification debt** — merging unvalidated output | Strongest area. The Gate blocks; the optional Shadow set *quantifies the Gate's own blind spot* as `regression_escape_rate` and emits `RegressionEscaped`. Measuring the ceiling is rarer than enforcing the floor. |
| **Cognitive surrender** — the human stops reading | Addressed: `require_approval` parks every verified proposal for `aoa approve` (ADR 008); terminal failures preserve the worktree and surface a "needs human" handoff in `aoa status`. |
| **Comprehension rot** — the mental model drifts from the code | Partly. `aoa status`, `aoa diagnose` (MAST histogram), and OTel traces exist; there is no per-run human digest. Deferred — reopen if `status` + `diagnose` prove insufficient on a real repo. |
| **Token blowout** — a failure spiral burns the budget | **This was a real hole, now fixed.** `Goal.TokensSpent` was only charged from `ProposalSubmitted` and `TicketDecomposed`, so every attempt that burned tokens *without* reaching a proposal — agent error, "no changes", commit failure — charged zero. The spend governor was blind to precisely the bimodal long tail it exists to bound. Failed and retried attempts now carry `Tokens`/`Model` and charge the Goal, and the USD ceiling records `LimitUSD`/`SpentUSD` so a cost trip is distinguishable from a token trip. |

## The one real gap: discovery

`aoa` does not find its own work. Goals arrive because a human typed `aoa goal` or `@aoa` in a comment.
Nothing reads a red CI run, an issue labelled `agent-ready`, or a failing nightly.

This is the model's "Blind Loop": execution is automated, triage is not. It is a genuine gap and it is
recorded as one rather than papered over.

What has been done instead of building a triager: the webhook path that *does* exist was made safe to
leave running — durable delivery deduplication via an idempotency key on `GoalSubmitted`, single-flight
runs so two deliveries cannot race the Event Log, real server timeouts, and errors that surface instead of
vanishing.

**Reopen gate.** Build discovery sources when the webhook path demonstrably isn't enough on a real repo.
The shape is already clear and does not need an LLM: a deterministic `Source` producing candidates with
idempotency keys, the first being `gate-red` — run the configured Gate against `main` and, if it fails,
submit one deduplicated Goal to fix it. That reuses `internal/verify` and needs no network. Whatever gets
built, **discovery stays deterministic Go**: an LLM deciding what the fleet works on next is a coordinator
by another name (ADR 003).

## What `aoa` deliberately refuses

Each of these is a live recommendation in the loop-engineering discourse. Each is declined here, with the
decision that settled it.

- **An adversarial maker–checker LLM sub-agent.** The Gate is a strictly stronger oracle, and the
  research's own case that models cannot self-correct is the argument for a deterministic checker, not for
  a second model. (ADR 002, 005, 011.)
- **A Ralph-style infinite bash loop.** `aoa` already *is* Ralph, with the sharp edges removed: every
  attempt gets a fresh worktree and a freshly assembled prompt (no conversation to rot), and objective
  compiler output is the only signal that carries forward. The difference is that the loop's state lives
  in an append-only log instead of a `fix_plan.md` the agent might mangle, termination is bounded by
  explicit gates rather than a heuristic circuit breaker, and nothing lands without passing the Gate.
- **Markdown files as memory** (the OpenClaw `SOUL.md` / `HEARTBEAT.md` model). A replayable event log
  strictly dominates: it gives crash recovery, audit, idempotency, and every metric as a pure projection.
  Conventions already cover the "durable instructions" half via `conventions_file`.
- **An AI gateway as a required dependency** (LiteLLM and similar). The per-Goal token and dollar
  governors run in-process, and `[backends].base_url` already points any backend at a gateway for teams
  that want one. Making a proxy mandatory would violate the one-binary constraint (AGENTS.md rule 6) for
  capability that is already present.
- **A supervisor daemon.** A long-lived controller would be a second control loop (ADR 003). `aoa run`
  exits cleanly and re-runs safely, so cron and systemd cover it — better, since they already solve
  restart, backoff, and logging.

## Where the harnesses fit

OpenHands, SWE-agent, and `mini-swe-agent` are frequently discussed as alternatives. They are not
competitors — they are **harnesses**, and they sit one layer *below* `aoa`. A harness arms a single agent
run: which tools it gets, how its terminal output is condensed, when the run is done. `aoa` schedules,
verifies, and merges many such runs.

The clean relationship is that a harness is an `agent.Backend` (ADR 004). `mini-swe-agent` in particular
would make an interesting A/B — it is the canonical minimal-scaffolding reference — but that is gated on
having a reproducible SWE-bench baseline to A/B *against*.

One number from that literature is worth carrying: on SWE-bench Verified, the strong harnesses resolve
well, but on [SWE-bench-Live](https://swe-bench-live.github.io/) — freshly opened, uncontaminated
issues — the same systems score far lower. Whatever the loop around them, agents are better at known ground than
at genuinely novel ambiguity. That is an argument for keeping the Gate strict and the human in the
approval path, not for trusting a longer loop.

## Scorecard

| Move | Where `aoa` stands |
|---|---|
| Discovery | Webhook only — deduped and single-flight. Autonomous sources are [deliberately deferred](roadmap.md#deliberately-deferred-with-reopen-conditions) |
| Handoff | Per-attempt git worktrees plus a serializing, gated merge queue |
| Verification | A deterministic Gate, with the blind spot itself measured (`regression_escape_rate`) |
| Persistence | Append-only event log, replay, and a deterministic context pack |
| Scheduling | `run --interval`, plus cron / systemd / GitHub Actions recipes |
| Cost control | Every attempt charged, including failed ones; token and USD ceilings per goal |
| Liveness | `Heartbeat` emitted throughout every agent run, so the Stall Detector measures silence |
| Termination gates | All reachable from `aoa.toml` |

## See also

- [`architecture.md`](architecture.md) §7 — what the design deliberately does not build
- [`comparison.md`](comparison.md) — design-level comparison against other coordination approaches
- [`../scheduling.md`](../scheduling.md) — cron, systemd, Actions, and webhook recipes
- ADRs [002](adr/002-verifier-gated-merge-queue.md), [003](adr/003-flat-orchestrator-worker.md),
  [005](adr/005-no-markets-no-consensus.md), [011](adr/011-debate-markets-as-offline-tools.md)
