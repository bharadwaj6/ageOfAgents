# Age of Agents vs. other agent-coordination approaches

How `aoa` compares to **Gastown** (role-hierarchy orchestration), **Spec Kit + plan mode** (single-agent,
spec-first), and **opencode "ultraworker"** (parallel-agent execution).

## What this document is — and is not

This is a **design-level** comparison grounded in each system's architecture, paired with the properties
`aoa` *proves about itself hermetically*. It is **not** a live head-to-head benchmark: running competitors
against the same tasks with a real LLM requires installing each, real API spend, and produces noisy,
hard-to-reproduce numbers (see `roadmap.md` for the rationale behind deferring that). Instead we compare
on the dimension the research says actually decides multi-agent success — **coordination, verification,
and idempotency**, not raw model capability or agent count (`docs/design/architecture.md` §1; Cemri et al.,
MAST, arXiv:2503.13657).

Two evidence sources back the `aoa` column:

- **The invariant harness** (`internal/invariant`, `internal/orchestrator/chaos_test.go`) — across 40
  seeded fault+crash histories, `aoa` provably maintains: main-always-green, serial single-writer merges,
  replay determinism + totality, no duplicate-merged idempotency key, an acyclic graph, and liveness.
- **The controlled benchmark** (`internal/bench`, `aoa bench`) — on the curated suite, every strategy
  records **0 coordination LLM sessions** and **100% merge correctness**, and the emergent strategy
  unlocks worker parallelism the single-agent / plan-first baselines cannot:

  | task | strategy | merged | workers (max ∥) | crit-path | coord-LLM | merge-correct |
  |------|----------|-------:|----------------:|----------:|----------:|--------------:|
  | chat-app | single | 1 | 1 | 1 | 0 | 100% |
  | chat-app | planfirst | 2 | 1 | 2 | 0 | 100% |
  | chat-app | emergent | 4 | 2 | 3 | 0 | 100% |

  (`single` and `planfirst` are in-tree stand-ins for naive single-agent and spec+plan workflows.)

## Property matrix

Rows are the `docs/design/metrics.md` litmus properties. "✓ proven" = asserted by the `aoa` test suite;
others are assessed from each system's documented architecture and may change as those systems evolve
(see Caveats).

| Property | aoa | Gastown | Spec Kit + plan | opencode ultraworker |
|---|---|---|---|---|
| **Coordination uses ~0 LLM** (deterministic scheduler) | ✓ proven (bench: 0 sessions) | ✗ — Mayor/Witness/Deacon are LLM agents that coordinate | ✓ (single agent; no separate coordinator) | ~ partial — parallel workers, but spawning/merging policy varies |
| **`main` never red** (gate-verified, serial merge queue) | ✓ proven (invariant I1/I2) | ~ has a Bors-style Refinery, but reported control failures auto-merged failing tests | ✗ — relies on the human + single agent; no enforced gate/queue | ~ depends on configured checks; no event-sourced guarantee |
| **Full event-replay / crash recovery** (log is truth) | ✓ proven (I3 + crash-recovery test) | ~ Dolt/Beads ledger is event-sourced, but state also lives in tmux/process trees (ZFC) | ✗ — no durable orchestration log | ✗ — session-scoped, to our knowledge |
| **No step repetition** (idempotency keys) | ✓ proven (I4) | ~ "Nondeterministic Idempotence" via supervision, not idempotency keys | ✗ — re-runs repeat work | ✗ — no idempotency contract |
| **Deterministic recovery** (one stall detector, crash-only restart) | ✓ (stall detector + restart-from-log) | ~ multi-tier escalation (Deacon→Mayor), heavier and LLM-driven | n/a (single agent) | ~ worker restarts, policy-dependent |
| **Safe parallelism** (isolated worktrees + serial merge) | ✓ proven (bench parallelism; merges stay serial/green) | ✓ (worktrees + Refinery) | ✗ — single agent, no parallelism | ✓ — parallel workers (its core feature) |
| **Small & portable** (one binary, JSONL, git only) | ✓ (2 core deps + opt-in OTel; no DB/broker) | ✗ — Dolt DB, tmux, OTEL, plugin sidecars | ✓ (a workflow, not infra) | ~ part of the opencode app |

## Per-system reading

- **Gastown.** Its engineering core is sound distributed systems in disguise (event-sourced ledger,
  serializing merge queue, supervision tree, failure detector, wrapped in a persona layer of
  Mayor / Witness / Deacon / Polecats). `aoa` keeps exactly those load-bearing primitives and **deletes the org-chart layer**:
  the personas (Mayor/Witness/Deacon/Polecats) are an anthropomorphic mapping of human-org limits onto
  aligned agents, and they put *LLMs in the coordination path* — which is where Gastown's reported chaos
  ($100/hr, auto-merged failing tests) comes from: a control failure, an under-strict gate. `aoa` makes
  the gate non-negotiable and the coordinator deterministic, so the same failure is impossible by
  construction.
- **Spec Kit + plan mode.** Strong on *specification* (the front half of the problem) and simple. But it
  is single-agent: no parallelism, no serializing merge queue, no event-sourced recovery, and
  decomposition is an up-front static plan rather than an emergent graph that adapts as the agent learns
  the codebase. `aoa` treats the spec/plan as input and adds the verification + idempotency + parallel-
  execution machinery the research identifies as the actual bottleneck.
- **opencode "ultraworker".** Closest to `aoa` on the *parallelism* axis and a genuinely good idea. The
  difference `aoa` argues for is the **substrate**: an append-only event log as the single source of
  truth, idempotency keys, and a verifier-gated serializing merge queue give provable correctness and
  crash recovery, rather than coordination/merging policy that is session-scoped and configuration-
  dependent. Where ultraworker is strong, `aoa` is strong *and* checkable.

## A different layer: harnesses and personal loops

Three systems come up constantly alongside `aoa` but are **not** in the matrix above, because they answer
a different question. Placing them precisely matters more than scoring them.

- **OpenHands and SWE-agent** (and its minimal descendant `mini-swe-agent`) are **harnesses**: they arm a
  *single* agent run — which tools it gets, how terminal output is condensed to survive the context
  window, when the run is finished. SWE-agent's Agent-Computer Interface is the sharpest statement of that
  idea. They sit one layer *below* `aoa`, which schedules, verifies, and merges many such runs. The clean
  relationship is that a harness is an `agent.Backend` (ADR 004), not a competitor — and `mini-swe-agent`
  would be a useful A/B backend once a reproducible SWE-bench baseline exists to compare against.
- **OpenClaw** is a **personal always-on assistant**: a gateway daemon bridging chat platforms, with
  memory as human-readable Markdown on disk (`SOUL.md`, `AGENTS.md`, `HEARTBEAT.md`) rather than a vector
  store. Its dynamic-context-assembly insight — pull specific files in on demand, don't rely on semantic
  similarity — is the same instinct behind `aoa`'s deterministic context pack. The divergence is the
  medium: `aoa`'s state is a replayable event log, which additionally yields crash recovery, idempotency,
  audit, and every metric as a pure projection. Markdown memory cannot give those.
- **Ralph** (the "run the same prompt in an infinite loop with fresh context" technique) is the closest
  thing to `aoa`'s dispatch model in spirit — every attempt starts clean, and objective compiler output is
  the only signal that carries forward. `aoa` is Ralph with the sharp edges removed: state lives in an
  append-only log rather than a plan file the agent edits, termination is bounded by explicit gates rather
  than a loop-detection heuristic, and nothing lands without passing the Gate.

A caution worth carrying: strong harnesses score far better on SWE-bench Verified than on
[SWE-bench-Live](https://swe-bench-live.github.io/), whose issues are freshly opened and therefore
uncontaminated by training data. The gap is large enough that SWE-bench-Live exists to measure it.
Whatever the loop around them, agents are far better on known ground than on genuine ambiguity — an
argument for a strict Gate and a human approval path, not for a longer leash. See
[`loop_engineering.md`](loop_engineering.md).

## Live evaluation protocol (closing the mock→live gap)

The matrix above is hermetic and architectural. The honest next step — repeatedly flagged in review — is
a **live, real-LLM** comparison. `aoa` now ships the harness for it (`internal/liveeval`, `aoa eval`,
ADR 009); running it at scale is a matter of API budget, not missing machinery. The protocol:

- **Benchmarks:** SWE-bench Lite (300 curated real GitHub issues) and/or a curated internal set. Each task
  supplies a repo + a **success oracle** (the issue's reproduce test) — exactly the `liveeval.Task` shape.
- **Conditions held equal:** same model, token budget, and tool set across every system.
- **Systems:** a single-agent baseline; `aoa` single / plan-first / emergent (the existing bench
  strategies, now runnable on the `claudecode` backend); and, budget permitting, gastown / opencode.
- **Metrics (all replay-derived):** task-success rate (success oracle), tokens (`metrics.TokensTotal`),
  wall-clock, rejected-proposal/rollback rate, and the **MAST failure-mode histogram** (`aoa diagnose`) —
  so we can show whether the design's failure-mode *prevention* survives a real agent, not just assert it.
- **Damage-rate on `main`:** how often a failing build/test reaches `main` per system — `aoa`'s should
  stay 0 by construction (invariants I1/I2), the differentiator versus LLM-coordinator stacks.

This is deliberately separated from the hermetic suite (ADR 009): the mock numbers prove correctness by
construction; the live numbers measure efficacy. We do not fold one into the other.

## What `aoa` does **not** claim here

- It does **not** claim higher task-success on a public coding benchmark — that needs live runs we have
  deliberately not done. The claim is narrower and stronger: on the properties that drive multi-agent
  failure (coordination/verification/idempotency), `aoa` holds invariants the others do not guarantee, and
  it does so *provably and hermetically*.
- The competitor columns are **architectural assessments**, not measurements, and those systems move fast
  (Gastown especially). Treat them as "what the design implies", to be revised against live evidence if
  and when we run it.
