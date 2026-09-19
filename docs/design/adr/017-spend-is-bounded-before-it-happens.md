# ADR 017: Spend Is Bounded Before It Happens

## Status
Accepted. Extends [ADR 015](015-aoa-is-a-backend.md) (front doors) and
[ADR 016](016-deliver-a-goal-as-a-pull-request.md) (delivery). Resolves
[#77](https://github.com/bharadwaj6/ageOfAgents/issues/77) for a single workspace.

## Context
Once aoa takes work from a front door, from issues labelled `aoa` for example, spend no longer follows
a human typing a goal. The only bounds were per-goal token and dollar caps, and they fell short:

- **Off by default and per goal only.** Nothing capped a run, a day or a workspace. Re-labelling an
  issue got a fresh goal, and so a fresh budget.
- **Enforced on lagging data.** The check ran before dispatch but counted only finished attempts, so
  attempts already in flight overshot the cap. With the defaults that is up to about 4 × a 30-minute
  agent session.
- **Leaky.** A failed agent call charged nothing, and neither did timeouts, stall restarts or rejected
  decompositions. A goal whose attempts all errored never tripped.
- **Priced wrongly.** One flat rate per model was applied to input, output and cache tokens alike. The
  cost the harness reports itself (`total_cost_usd`) was ignored, and the documented pricing keys never
  matched a real model id.
- **Two accounting paths**, one for the governor and one for `status`, with no test that they agree.

Unattended spend also has a cost that isn't dollars. On a subscription, automation draws on the same
quota windows as the person's own interactive work.

## Decision
**Every budget is enforced before work starts, by the one Scheduler, from the Event Log. Automation
runs under a budget or not at all.**

1. **The meter is the harness's own cost.** Where a harness reports it (claude and codex report
   `total_cost_usd`), that figure is what is charged. `[pricing]` × tokens is only a fallback for
   harnesses that don't. Tokens are always recorded too. On a subscription the dollars are notional,
   but they are accurate and comparable across runs.
2. **Every attempt is charged, including failures.** A harness's output is read even when it exits
   non-zero, so an erroring attempt still reports what it spent. The events that carry spend gain an
   additive `cost_usd`. One accounting function feeds both the governor and `status`, and a test holds
   them together.
3. **Budgets have scopes**, all checked before a new attempt is dispatched or a new goal is started:

   | Scope | Knob | What it bounds |
   |---|---|---|
   | attempt | the harness's own cap, e.g. `--max-budget-usd` in `[backends.<name>].args` | the only bound on an attempt already running, besides `agent_timeout` |
   | goal | `max_usd_per_goal`, `max_tokens_per_goal` | one goal |
   | run | `aoa run --max-usd --max-tokens --max-goals` | one supervised session |
   | day | `[budget] usd_per_day`, `tokens_per_day`, `goals_per_day` (UTC day, from event timestamps) | a day of runs in the workspace |

   A goal counts toward `goals_per_day` / `--max-goals` when its first task is created. A goal that
   hasn't started stays `queued` and costs nothing.
4. **When a limit is reached, nothing new starts.** The Scheduler dispatches no new attempt and starts
   no new goal. It appends one `BudgetExhausted{scope, spent_usd, limit_usd, spent_tokens,
   limit_tokens, goals, limit_goals}`. Attempts already running finish, because stopping them would
   waste spend already made. **The honest worst case is the limit plus `concurrency` × the per-attempt
   cap.** That is why the per-attempt cap matters, and why a tight budget wants `concurrency = 1`.
5. **A workspace can refuse to run unbudgeted.** With `[budget] require_run_budget = true`, `aoa run`
   will not start without `--max-usd` (exit 2).
6. **Subscription quota is the front door's check, not the core's.** Quota windows are specific to one
   vendor, so reading them (for example through `quota-axi`) belongs to the front door or wrapper that
   decides whether to start a run (ADR 015). The core meters and enforces spend it can observe.
7. **Automation is supervised.** The reference front door runs as a command a person starts with a
   budget (`AOA_RUN_MAX_USD`), not as a scheduler. It caps how many goals it starts per cycle. A
   scheduled front door needs its own explicit budget and its own decision to exist.

## Consequences
- **New replay events.** `BudgetExhausted` is a new event, and `cost_usd` joins the payloads that carry
  spend. Both are additive, but a log containing `BudgetExhausted` cannot be read by an older binary.
- **Visibility.** `status --json` gains a `budget` block (day spend, limit, remaining), and `aoa run`
  reports what the run spent.
- **Correct pricing docs.** They move from preset names to model ids, and the claim that errored
  attempts were already charged becomes true.
- **Rejected alternatives:**
  - A per-goal human approval before any spend. It is supported in spirit by running manually, and it
    can be added later as its own gate.
  - Cancelling in-flight attempts when a budget trips.
  - Budgets spanning several workspaces. Which log owns them is still open (#131).
- **Tradeoff.** "Bounded" means bounded by a known formula, not exactly at the limit. aoa cannot stop
  a harness mid-call except by its timeout, so the per-attempt cap has to be the harness's own.

## Research basis
Engineering judgement and one day of evidence: aoa's first self-hosted run, and a survey of where the
old governors leaked. The case for enforcing in the deterministic Scheduler, from the log, is ADR 001's
and ADR 003's.
