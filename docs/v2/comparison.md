# Age of Agents vs. other agent-coordination approaches

How `aoa` compares to **Gastown** (role-hierarchy orchestration), **Spec Kit + plan mode** (single-agent,
spec-first), and **opencode "ultraworker"** (parallel-agent execution).

## What this document is — and is not

This is a **design-level** comparison grounded in each system's architecture, paired with the properties
`aoa` *proves about itself hermetically*. It is **not** a live head-to-head benchmark: running competitors
against the same tasks with a real LLM requires installing each, real API spend, and produces noisy,
hard-to-reproduce numbers (see `roadmap.md` for the rationale behind deferring that). Instead we compare
on the dimension the research says actually decides multi-agent success — **coordination, verification,
and idempotency**, not raw model capability or agent count (`docs/v2/architecture.md` §1; Cemri et al.,
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

Rows are the `docs/v2/metrics.md` litmus properties. "✓ proven" = asserted by the `aoa` test suite;
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
| **Small & portable** (one binary, JSONL, git only) | ✓ (1 dep; no DB/broker) | ✗ — Dolt DB, tmux, OTEL, plugin sidecars | ✓ (a workflow, not infra) | ~ part of the opencode app |

## Per-system reading

- **Gastown.** Its engineering core is sound distributed systems in disguise (event-sourced ledger,
  serializing merge queue, supervision tree, failure detector — see `docs/history/gastown_arch.md` and
  `docs/claude.md`). `aoa` keeps exactly those load-bearing primitives and **deletes the org-chart layer**:
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

## What `aoa` does **not** claim here

- It does **not** claim higher task-success on a public coding benchmark — that needs live runs we have
  deliberately not done. The claim is narrower and stronger: on the properties that drive multi-agent
  failure (coordination/verification/idempotency), `aoa` holds invariants the others do not guarantee, and
  it does so *provably and hermetically*.
- The competitor columns are **architectural assessments**, not measurements, and those systems move fast
  (Gastown especially — see `docs/claude.md` caveats). Treat them as "what the design implies," to be
  revised against live evidence if/when we run it.
