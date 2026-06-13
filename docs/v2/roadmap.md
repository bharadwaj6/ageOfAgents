# Age of Agents — v2 Implementation Roadmap

**Status:** Tracks A, B, C complete (emergent graph + cycle/runaway guards; hermetic invariant +
fault-injection harness; replay-based metrics + benchmark + design comparison). This is the living plan +
progress tracker and the **resume anchor**: a fresh agent (e.g. if the session runs out of credits) should
start here. Next candidates (not yet started): live head-to-head competitor runs (deferred — see
`comparison.md`), an optional TLA+ spec, and `claudecode` real-LLM bench runs.

## Resuming agent: start here

1. Read this file, then `AGENTS.md`, `docs/v2/architecture.md`, and the ADRs in `docs/v2/adr/`.
2. Confirm a green baseline: `go build ./... && go vet ./... && go test ./...` and `gofmt -l cmd internal pkg`.
3. Continue at the **first unchecked `[ ]` item** below. Update its checkbox + "last status" line as you go.
4. Commit working states frequently (Conventional Commits, ≤72-char subject, no AI model names in
   subject/body). **Push before and after each step** so origin always reflects current progress.

## Why this phase

v2 is a clean, green, event-sourced Gate-verified orchestrator (~3k LOC). But three things the design
*claims* are not yet real:

1. **Emergent task graph (ADR 006) is documented, not built.** `orchestrator.ReconcileOnce` creates
   exactly one ticket per goal (`g.ID+"-impl"`) — no decomposition. `agent.Result` has no field for a
   worker to return subtasks; the orchestrator never emits `TicketCreated` from worker output;
   `TicketCreatedPayload.CreatedBy` is dead. The "diamond" graph in `scripts/benchmark_coordination.sh`
   is hand-injected JSONL. There is **no cycle detection** on `depends_on` — once emergence lands, a
   confused agent can deadlock the scheduler.
2. **The `metrics.md` litmus invariants are asserted nowhere** — main-never-red, replay fidelity,
   idempotency/no step-repetition, crash recovery. No fault injection, property tests, or ledger
   torn-write / concurrent-append stress.
3. **No benchmark framework** — two ad-hoc shell scripts, no reproducible harness computing the
   `metrics.md` numbers, no comparison.

**Goal:** build the emergent graph, then *prove* correctness properties hermetically (Jepsen-style
invariants + fault injection), then *measure* against honest baselines and compare by design to
gastown / speckit+plan / opencode ultraworker.

**Decisions (locked with the user):** order **A → B → C**; proof depth = **hermetic proofs + controlled
bench + design comparison** (no live competitor runs, no TLA+); task suite = **Go-native curated +
multi-component decomposition tasks**.

**Non-negotiables (ADRs 001–006):** Event Log is truth (state derived by replay); nothing merges without
the Gate; one deterministic Scheduler (no role hierarchy, no LLM coordinator); all LLM access via
`agent.Backend`; no markets/voting/consensus; coordinate via the Shared Log. Tests stay hermetic/offline
(the `mock` Backend never makes network calls).

---

## Track A — Emergent task graph + graph-safety guards

**Decomposition model (decided; alternatives weighed).** A worker assigned a coarse ticket may decide it
is too big and return child subtasks instead of a diff. Model the parent's outcome as a new terminal
status **`StatusDecomposed`** via a new **`TicketDecomposed`** event. (Alternatives: *parent-merges-empty*
pollutes git history and conflates "no changes = failure"; *children-as-new-goal* loses parent→children
provenance + the dependency join. `TicketDecomposed` is the smallest event-sourced change and keeps the
join clean.) This is a *worker* extending the graph through the Shared Log — consistent with ADR 006 and
ADR 003 (no separate LLM coordinator). Reuse existing idempotency dedup in `state.Apply` and the
`DepsSatisfied`/`NewlyReady` readiness logic.

- [x] `pkg/api/events.go`: `TicketDecomposed` event + `TicketDecomposedPayload{TicketID, Worker, Children}`;
      `Depth` field on `TicketCreatedPayload`.
- [x] `internal/agent/agent.go`: `Result.Subtasks []Subtask`
      (`Subtask{LocalID, Title, DependsOn []string, IdempotencyKey}`; `DependsOn` references sibling
      `LocalID`s or existing ticket IDs; orchestrator resolves to real ticket IDs).
- [x] `internal/agent/mock.go`: deterministic `Decompose map[string][]Subtask` plan so the diamond
      graph is produced *by the system* in `go test`, not injected.
- [x] `internal/agent/claudecode.go`: `BuildPrompt` documents the fenced `aoa:subtasks` JSON block; `Run`
      parses it into `Result.Subtasks` (no diff + subtasks = decompose). Parser + tests added.
- [x] `internal/state/state.go`: `StatusDecomposed` (+ in `IsTerminal`); `TicketDecomposed` in `Apply`;
      `Ticket.Children`/`Depth`; completion-aware `DepsSatisfied`/`ticketComplete` (a decomposed parent
      completes when all descendants merge); `HasCycle`/`WouldCycle` guards; `DeadDependency`/`Blocked`
      for dead-dependency liveness (incl. death through a decomposed subtree).
- [x] `internal/orchestrator/orchestrator.go`: `decompose()` resolves LocalID->ticketID, rejects
      missing/duplicate LocalID, dangling deps, cycles, and over-budget; emits `TicketCreated`
      (`CreatedBy`, `Depth+1`, deduped) per child then `TicketDecomposed`; no-diff *without* subtasks still
      fails. `Options.MaxGraphDepth` (5) + `Options.MaxTicketsPerGoal` (64) governors; new ReconcileOnce
      step fails `Blocked()` tickets. Termination covers `Decomposed` via `IsTerminal`.
- [x] Tests: `state_test.go` (HasCycle table, WouldCycle, decomposed completion, dead-dep liveness +
      through-decomposition); `orchestrator_test.go` (`TestRunEmergentDiamondDecomposition`,
      `TestRunRejectsCyclicDecomposition`); `agent_test.go` (subtask parse / no-subtask cases).

**Last status:** done. Build/vet/test green, gofmt clean. Diamond decomposition runs end-to-end on the
mock backend in-process. Committed (see git log).

## Track B — Hermetic Jepsen-style invariant + fault-injection harness

Single-node, so this is the *spirit* of Jepsen: define invariants, inject faults, check invariants across
many randomized histories. Offline/deterministic; no new third-party dependency (linearizability reduces
to a single serial merge log → a lightweight in-tree checker suffices; Porcupine noted only as a future
multi-writer option).

- [x] `internal/invariant/invariant.go` — pure checkers over `[]api.Event` (+ repo), reusing `state.Fold`,
      `verify.Verifier`: `MergeImpliesVerified` (I1 log-level), `MainGreen` (I1 git-level),
      `MergedAtMostOnceByQueue` (I2), `ReplayDeterministicAndTotal` (I3), `NoDuplicateMergedKey` (I4),
      `AcyclicGraph` (I5), `MonotonicGaplessSeq` (log integrity), `Settled` (I6 liveness). `Check()`
      aggregates the pure ones. Unit-tested against healthy + crafted-bad histories.
- [x] `internal/agent/faulty.go` — `Faulty` Backend (seeded PRNG) injecting: error, no-change, conflict
      (shared file → merge-queue rollback), bad-verify (sentinel → post-merge verify fail → rollback),
      cyclic subtasks (→ cycle guard), duplicate-key subtasks (→ dedup). (Stall is exercised via the
      crash-recovery test, not a hanging backend — dispatch is synchronous.)
- [x] `internal/orchestrator/chaos_test.go` — `TestChaosFaultInjection`: 40 seeds (8 under `-short`), each
      runs a diamond goal through the faulty backend to completion and asserts **all** invariants + main
      green + settled. `TestCrashRecoveryFromLedger`: an abandoned in-flight ticket (claimed+started in the
      past) is recovered by a fresh orchestrator via stall detection → merge, invariants intact.
- [x] `internal/ledger/ledger.go` + `_test.go` — **hardened**: `Read`/`Open` tolerate a torn trailing line
      and `Open` truncates it (crash-safety; previously a torn write bricked the log). Tests: torn-tail
      tolerance + repair, gapless concurrent-append stress (300 goroutines), mid-log corruption errors.

> **Bug found & fixed by the chaos harness:** duplicate-idempotency-key subtasks made `decompose()` list a
> phantom child that `state.Apply` deduped away, so the parent waited forever (liveness violation). Fixed
> by collapsing duplicate keys onto one canonical child and adopting children whose key already exists in
> state (also makes re-decomposition after a crash idempotent). Added `state.TicketForKey`.

**Last status:** done. Full suite + 40-seed chaos green, gofmt clean, committed.

## Track C — Replay-based metrics + controlled benchmark + design comparison

- [x] `internal/metrics/metrics.go` — computes the `metrics.md` numbers purely by replaying the Event Log:
      coordination sessions (0 by design), merge correctness, rejected-proposal rate, step-repetitions,
      mean attempts-to-merge, **max concurrent workers** (parallelism achieved), **critical-path depth**,
      duration + throughput, emergent-ticket count. Unit-tested on a diamond + a retry history.
- [x] `internal/bench` — Go-native curated suite (chat-app / lru-cache / cli-tool) run under three
      strategies on the deterministic mock: `single`, `planfirst`, `emergent` (the diamond). Reuses the
      orchestrator + invariant + metrics packages; asserts 0 violations and that emergent beats the
      baselines on parallelism. (Replaces `bench/tasks/`; tasks are defined in `bench.Suite()`.)
- [x] `cmd/aoa bench` subcommand — runs the suite in a temp workspace, emits a markdown table (or
      `--json`) of the metrics + violations per task × strategy. `scripts/benchmark_coordination.sh` is now
      a thin wrapper over it (`benchmark_live.sh` stays as the real-`claudecode` E2E).
- [x] `docs/v2/comparison.md` — design-level property matrix vs gastown / speckit+plan / opencode
      ultraworker, grounded in `docs/history/gastown_arch.md` + the research corpus, paired with the
      properties `aoa` proves about itself (invariant harness + bench). Clear about what is/ isn't claimed.
- [x] `docs/v2/adr/007-emergent-decomposition-and-graph-governor.md` — the structural decision from
      Track A (TicketDecomposed + graph governor + completion/liveness), indexed in the ADR README.

**Last status:** done. Full suite green (incl. metrics + bench tests), gofmt clean, committed.

---

## Verification (end-to-end)

- Each increment: `go build ./... && go vet ./... && go test ./...` green; `gofmt -l cmd internal pkg`
  empty. Small increments; stop & re-plan if a change produces 20+ errors.
- **Track A:** `go test ./internal/orchestrator -run Diamond -v` shows a goal decomposing into a diamond,
  running in parallel, joining, and merging on the `mock` backend, offline.
- **Track B:** `go test ./internal/orchestrator -run TestChaos -count=200` (or a `chaos` build tag) runs
  many seeded fault+crash schedules with **zero** invariant violations; `go test ./internal/invariant`
  and `go test ./internal/ledger -run 'Torn|Concurrent'` pass.
- **Track C:** `go run ./cmd/aoa bench --backend mock` produces a JSON + markdown report on the curated
  suite, asserting coordination-tokens=0, merge-correctness=100%, step-repetition=0; baselines appear as
  comparison rows. Optionally one env-gated `claudecode` run for a real-LLM sanity check (out of the
  hermetic suite).
- The four-point litmus from `metrics.md` (zero coordination LLM sessions, main never red, full
  event-replay, green offline suite) becomes machine-checked, not just asserted in prose.
