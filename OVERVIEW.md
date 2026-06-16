# Overview — what Age of Agents is, aims to be, and still needs

A high-level orientation for newcomers and contributors. For the elevator pitch see
[`README.md`](README.md); for the design and its research basis see
[`docs/design/architecture.md`](docs/design/architecture.md) and the ADRs in
[`docs/design/adr/`](docs/design/adr/).

## What it does

Age of Agents (`aoa`) is a **deterministic, Gate-verified build system whose compile step is a stochastic
LLM** — "Bors for AI coding agents." You give it a **Goal** in plain English; it decomposes the Goal into
**Tasks**, dispatches each to a **Worker** (an agent in its own isolated git worktree), and merges a
Worker's candidate diff into `main` **only if your build and tests pass**. One static binary, one config
file (`aoa.toml`), git only — no database, no broker, no message bus, no LLM coordinator.

The load-bearing properties, each with proofs attached:

- **The Event Log is the single source of truth.** An append-only JSONL ledger; all state — task
  readiness, worker status, the merge queue — is derived by *replaying* it (`internal/state`). Crash
  recovery, audit, and every metric come for free.
- **A verifier-gated, serializing merge queue** keeps `main` linearizable and always green: merge → run
  the Gate on the post-merge state → keep it only if it passes, else roll back. Disjoint-file proposals
  are batched into one Gate run.
- **One deterministic Scheduler**, not a swarm of chatting agents. No LLM in the control plane; agents
  coordinate only through the shared log, never by messaging each other.
- **Honest about its own ceiling.** Because the system is only as good as its Gate, it *measures* the
  blind spot: a regression-escape rate (merges the Gate accepted but a broader shadow suite would reject)
  and a per-run **MAST** failure-mode histogram (`aoa diagnose`).
- **Cost-aware and bounded.** Token/`$` accounting per ticket and per goal, a per-goal spend governor
  (circuit breaker), retry backoff + crash-loop detection, and a `--max-cost` ceiling for eval runs.
- **OpenTelemetry-native, vendor-agnostic.** Every run replays into OTLP traces (goal → ticket → attempt
  spans) and metrics — post-hoc (`aoa otel export` / `--otel`) or live during a run (`--otel-live`) —
  pointed at Honeycomb, Tempo, Datadog, or any OTLP backend via standard env vars. Off by default.
- **Adoptable and recoverable.** Point it at your own repo on any branch (`aoa init --adopt`, Gate
  auto-detected); on a terminal failure it preserves the agent's worktree and hands it back to you.

Proven, not asserted: a hermetic Jepsen-style invariant harness with seeded fault injection, plus a TLA+
model of the merge/approval invariants. The whole test suite is offline — the `mock` Backend never
touches the network.

## What it aims to be

The thesis: **verification, not intelligence, is the scaling constraint for agentic coding.** Most agent
frameworks chase orchestration cleverness (role hierarchies, debate, consensus) — exactly the part that
better models erode. `aoa` bets the opposite way: keep scheduling, state, merge, and done-ness as plain
deterministic Go gated on objective signals (your build, your tests, the compiler), and let the LLM only
ever emit a *candidate diff* that the Gate, not the agent, decides on. Better models then only sharpen the
worker; the control plane is unchanged.

The goal is to be a **tool engineers use daily on real repositories** — not a demo. That means: correct
by construction (done), cost-bounded and observable so you can run it on real money and see what happened
(done), adoptable into an existing project in minutes (done), and **empirically validated at scale**. 
We've achieved the latter, landing a 50% pass@1 solve-rate on a verified 20-instance SWE-bench Lite subset, allowing future architectural changes to be honestly A/B tested.

## Where it needs more work

Roughly in priority order. Tracked against the GitHub `v0.1 — measured & adoptable` milestone.

1. **Multi-file / cross-cutting work.** Emergent decomposition + disjoint-file batching handle
   single-file, Lite-style tasks well. The other half of a dynamic dependency DAG — recomputing edges
   after each merge — is unbuilt; it only pays off past single-file work.
2. **A `$` governor in the control plane.** The orchestrator has a *token* spend governor; a true *dollar*
   circuit breaker (vs. the eval-loop `--max-cost`) is a follow-up. Live OTel streaming also drops spans
   silently if the export queue saturates (the append hook is non-blocking by design) — fine now, worth a
   metric later.
3. **More backends & richer integrations.** We have `mock`, `claudecode`, `anthropic`, and `grok`; OpenAI/Gemini/local
   models would broaden reach. No shipped dashboards-as-code (Grafana/Honeycomb boards) yet.
4. **Deferred research bets (#16–#18), closed with explicit reopen gates:** speculative/batched merge with
   an adaptive window, best-of-N with the test suite as verifier, and SPRT early-stopping for live evals.
   Now that we have a SWE-bench baseline, these can be reopened and A/B tested to measure their impact.

If you're picking this up: you can use our new SWE-bench Lite baseline to start measuring the impact of the deferred research bets (#4). Everything else is incremental.
