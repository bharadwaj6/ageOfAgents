# Age of Agents — v2 Implementation Roadmap

**Status:** in progress. This is the living plan + progress tracker for the next phase of work. It is the
**resume anchor**: a fresh agent (e.g. if the current session runs out of credits) should start here.

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

- [ ] `internal/invariant/invariant.go` — pure checkers over `[]api.Event` (+ repo where needed), reusing
      `state.Fold`, `verify.Verifier`, `worktree.Repo`:
  - I1 **Main-always-green**: gate passes on main HEAD after every `Merged`.
  - I2 **Single-writer / serial merges**: merges totally ordered by `seq`; only the queue wrote main.
  - I3 **Replay determinism & prefix-closure**: `Fold` total + deterministic; any prefix folds valid.
  - I4 **Idempotency / no step-repetition**: no two merged tickets share an idempotency key; re-running
       the reconciler on a settled log emits nothing.
  - I5 **DAG acyclic** at all times.
  - I6 **Liveness**: every goal reaches terminal (merged/failed/decomposed) or honest no-progress; no
       cyclic/dead-dep stall.
- [ ] `internal/agent/faulty.go` — `Faulty` Backend wrapping another (seedable PRNG): fail, hang (trigger
      Stall Detector), no changes, write a conflicting file (force merge-queue rollback), emit duplicate
      subtasks, emit cyclic subtasks.
- [ ] `internal/orchestrator/chaos_test.go` — for many seeds, run under a randomized fault schedule **plus
      crash-restart** (drop in-memory state, re-`New()`+`Run()` from the durable ledger), assert all of
      I1–I6. Reuse the existing `setup`/`harness` helpers.
- [ ] `internal/ledger/ledger_test.go` — torn-write tolerance (truncated trailing JSONL line on `Read`
      skipped/recovered; harden `ledger.Read` if needed) + concurrent-append stress (gapless monotonic
      `seq`, no corrupt lines, final `Fold` succeeds).

**Last status:** blocked on Track A.

## Track C — Replay-based metrics + controlled benchmark + design comparison

- [ ] `internal/metrics/metrics.go` — compute `metrics.md` metrics purely by replaying the Event Log
      (coordination tokens **assert 0**; merge correctness **assert 100%**; rejected-proposal rate;
      step-repetition **assert 0**; recovery time; mean attempts-to-merge; throughput/worker).
- [ ] `bench/tasks/` — Go-native curated suite (self-contained tasks, `go build`/`go test` gates) +
      multi-component "diamond" tasks (shared types → backend + frontend → integration).
- [ ] `cmd/aoa` `bench` subcommand — run the suite, emit JSON + markdown report from `internal/metrics`,
      with built-in honest baselines `single` (one ticket, no decomposition) and `planfirst`
      (plan-then-implement single agent). Fold the two `scripts/benchmark_*.sh` into thin wrappers.
- [ ] `docs/v2/comparison.md` — design-level comparison table vs gastown / speckit+plan / opencode
      ultraworker: which `metrics.md` invariants each architecture can/can't guarantee, grounded in
      `docs/history/gastown_arch.md` + the research corpus.
- [ ] New ADR if Track A/B introduces a structural decision (e.g. graph governor / `TicketDecomposed`).

**Last status:** blocked on Track B.

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
