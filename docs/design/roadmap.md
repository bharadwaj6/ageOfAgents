# Age of Agents — Implementation Roadmap

**Status:** Tracks A, B, C complete (emergent graph + cycle/runaway guards; hermetic invariant +
fault-injection harness; replay-based metrics + benchmark + design comparison). **Track D complete**
(external-critique response: MAST self-measurement, human-in-the-loop approval gate, live-eval harness,
TLA+ spec + ADRs 008–011). This is the living plan + progress tracker and the **resume anchor**: a fresh
agent (e.g. if the session runs out of credits) should start here. Next work is **Phase E — measured &
adoptable** (see below; GitHub milestone v0.1, issues #4–#18): land a reproducible cost-first SWE-bench
number, add spend/safety governors, measure the verification blind spot, and make `aoa` adoptable on a
real repo.

## Resuming agent: start here

1. Read this file, then `AGENTS.md`, `docs/design/architecture.md`, and the ADRs in `docs/design/adr/`.
2. Confirm a green baseline: `go build ./... && go vet ./... && go test ./...` and `gofmt -l cmd internal pkg`.
3. Continue at the **first unchecked `[ ]` item** below. Update its checkbox + "last status" line as you go.
4. Commit working states frequently (Conventional Commits, ≤72-char subject, no AI model names in
   subject/body). **Push before and after each step** so origin always reflects current progress.

## Why this phase

The system is a clean, green, event-sourced Gate-verified orchestrator (~3k LOC). But three things the
design *claims* are not yet real:

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
- [x] `docs/design/comparison.md` — design-level property matrix vs gastown / speckit+plan / opencode
      ultraworker, grounded in `docs/history/gastown_arch.md` + the research corpus, paired with the
      properties `aoa` proves about itself (invariant harness + bench). Clear about what is/ isn't claimed.
- [x] `docs/design/adr/007-emergent-decomposition-and-graph-governor.md` — the structural decision from
      Track A (TicketDecomposed + graph governor + completion/liveness), indexed in the ADR README.

**Last status:** done. Full suite green (incl. metrics + bench tests), gofmt clean, committed.

## Track D — External-critique response

Three independent reviews converged: the architecture is well-aimed, but the strength was "hermetic + mock"
with no live evidence, no MAST *measurement*, no human checkpoint, and an under-stated formal/idempotency
story. This track addresses the real, actionable items (and skips what reviewers agreed to keep deferred:
multi-node, log compaction, markets/voting).

- [x] **MAST self-measurement** — `internal/diagnose` classifies a run's Event Log into a MAST failure-mode
      histogram (step repetition, premature termination, dead-dependency stall, retry churn, worker stall,
      missing verification) as a pure replay function; `aoa diagnose [--json]`; a `MAST` column on
      `aoa bench`; metrics.md documents the mode→signal mapping.
- [x] **Human-in-the-loop approval gate (ADR 008)** — opt-in `require_approval`; merge queue `DryRun`
      (merge→verify→always roll back) presents a Gate-verified candidate without writing to main;
      `ApprovalRequested/Granted/Denied` events + `awaiting` state; `aoa approve`/`aoa reject`; new
      `ApprovalGate` invariant; `Run` pauses cleanly when only approvals remain.
- [x] **Live-eval harness (ADR 009)** — `internal/liveeval` runs the orchestrator end-to-end on a real repo
      against a success oracle, backend-agnostic (hermetic with mock, opt-in `claudecode`); `aoa eval`;
      token usage threaded `agent.Result`→events→`metrics.TokensTotal`; comparison.md live-eval protocol.
- [x] **Formal & docs hardening** — `docs/design/formal/Orchestrator.tla` (+ `.cfg`) model-checks I1/I2/I4
      + the approval gate (TLC: 518 states, no error); ADR 010 (semantic idempotency) and ADR 011 (debate/
      markets rejected *as a live control plane*, not universally); architecture §7 reworded.

**Last status:** done. Full suite + chaos green, gofmt clean, TLC clean. Committed.

### Track D follow-up — first live run + SWE-bench adapter

- [x] **First real-LLM run, green.** `scripts/live_smoke.sh` drives the `claudecode` backend to fix a
      seeded failing test; the agent's change passes the Gate and merges, `aoa diagnose` clean. Documented
      in `docs/design/live_eval.md`.
- [x] **Backend bug found & fixed.** Headless `claude -p` declined to edit files without a permission mode
      (every Task failed "agent produced no changes"); the `claudecode` backend now defaults to
      `--permission-mode acceptEdits`, regression-guarded by `TestNewClaudeCodeAllowsEdits`.
- [x] **SWE-bench Lite adapter.** `scripts/swebench_to_tasks.py` clones each instance at its base commit,
      normalizes it to a `main` branch, and emits an `aoa eval` `tasks.toml` (Goal = problem statement;
      Gate + oracle = FAIL_TO_PASS). `scripts/eval_swebench.sh` wraps it. Env provisioning stays the
      caller's job (ADR 009).
- [ ] **Run SWE-bench Lite at scale** (needs API budget + a prepared Python env / SWE-bench Docker). The
      machinery is ready; this is the remaining empirical step.

---

## Phase E — from prototype to measured & adoptable

Tracks A–D are done. The system *proves things about itself* (hermetic, mock-backed) but does not yet
*help anyone*: every headline number is from the `mock` backend, and the one differentiator — driving real
agents to merged, correct code — is the unrun box above. This phase closes the gap from "clean prototype"
to "tool engineers use daily." Work the issues in priority order; each is a clean A/B on
solve-rate / cost / latency against the #4 baseline.

GitHub: milestone **v0.1 — measured & adoptable**. Resume at the lowest-numbered open issue whose deps
are met.

**P0 — the baseline that unblocks everything**
- [x] **#4** Run SWE-bench Lite at scale — reproducible (pinned models + image digests), pass@1 / pass@k,
      cost-per-solve, tokens-per-solve, MAST histogram → README. *The whole ballgame.*

**P1 — cost & safety (real-money table stakes)** — *done (PRs #20, #23, #22)*
- [x] **#5** Spend governor: per-goal token/$ ceiling + circuit breaker (the $100/hr failure mode).
- [x] **#6** Retry backoff + crash-loop detection (flaky vs. fundamentally broken).
- [x] **#7** Cost & latency as first-class metrics (per-ticket breakdown). *Unblocks the cost columns in #4.*

**P1 — the verification ceiling (the honest gap)**
- [x] **#8** Measure the verification blind spot — regression-escape rate (Merge Queue `Shadow` set +
      `RegressionEscaped` event + `regression_escape_rate` metric) + a coupled multi-file conflict test.

**P1 — adoption ergonomics**
- [x] **#9** `aoa init --adopt` an existing repo/branch + Gate auto-detect (worktrees cut from `HEAD`, so
      any branch works; never clobbers `aoa.toml`).
- [x] **#10** Warm handoff on terminal failure — terminal `TicketFailed` preserves the worktree (retries
      still clean up) and `aoa status` shows a "needs human" section with the reason + `cd <worktree>`.

**P2 — steering, observability, positioning**
- [x] **#11** Mid-run goal amendment (`GoalAmended` event) — `aoa amend <goal> "..."` appends guidance to
      the goal's effective text for future dispatches/retries (never preempts a running worker); adds the
      `stale_spec_drift` diagnose mode deferred from #14.
- [x] **#12** Live `aoa status --watch` (poll loop, no daemon, stops on settle); `--type` filter moved
      into `aoa events` (tail|replay), `feed` folded to a deprecated alias via a shared renderer.
- [x] **#13** Instrument the merge queue (`merge_queue_max_depth` + `wait_{mean,max}`, pure from the log,
      shown in `aoa status`) + batch disjoint-file proposals into one Gate run with serial fallback
      (skipped when an approval gate or shadow set is in play). Speculation stays deferred (#16).
- [x] **#14** Instrument the deterministic-orchestration failure taxonomy (extend `diagnose`):
      queue_starvation, scheduler_deadlock, retry_livelock, verification_blind_spot (stale_spec_drift
      pending #11).
- [x] **#15** README/positioning reframe ("Bors for AI agents, with the receipts").

**Observability & adoption (OTel-native + real-world usability)** — *done (PRs #38, #39, #40, #41)*
- [x] **#33** OpenTelemetry-native observability via replay-to-OTel — `internal/otel` projects the Event
      Log into OTLP traces (goal → ticket → attempt spans) + metrics (`aoa.*`, incl. MAST + `cost_usd`),
      off by default, vendor-agnostic (`aoa otel export` / `aoa run --otel`). ADR 012.
- [x] **#34** Honeycomb integration — `scripts/otel_smoke.sh` + `docs/integrations/honeycomb.md`; proven
      through standard OTLP env vars, no vendor code.
- [x] **#35** Cost-sensitive eval — `aoa eval --price-file` (per-model $), `--max-cost` (ceiling, halts
      between tasks), per-task $ + footer, `--otel` (each task its own trace). *Unblocks a cost-capped #4.*
- [x] **#36** Real-world adoption docs — config reference, integrations index, worked `examples/`.
- [x] **#37** Live per-append OTel streaming — `internal/otel.Live` streams the goal → ticket → attempt
      tree as events are appended (ledger append hook + `aoa run --otel-live`); seeds in-flight work from
      the existing log, off by default. Post-hoc `--otel` still available.

**P3 — deferred (closed as not-planned; reopen when the gate is met)**
- [x] **#16** Speculative/batched merge with an adaptive window — *closed.* #13 shipped the instrumentation
      (`merge_queue_wait_*`) + the cheap disjoint-batcher. **Reopen when** `merge_queue_wait_mean` climbs
      with depth staying high (serialization demonstrably the bottleneck). No AIMD window before then.
- [x] **#17** Best-of-N with the test suite as verifier — *closed.* Cost plumbing exists (#7). **Reopen
      when** #4 has a reproducible pass@1 + cost-per-solve baseline to A/B against (track $/solve or it's a
      trap).
- [x] **#18** SPRT early-stopping for live evals — *closed.* **Reopen when** at-scale live evals (#4) are
      routine and their token cost is worth cutting short. No correctness impact; strictly lower priority.

**Retired priors** (raised in review, already satisfied — do not re-open): *delete the leader daemon* (it
is only the grok-CLI auth bootstrap, not an LLM in the control plane); *build deterministic control-plane
tests* (the chaos harness + TLA+ spec + hermetic `mock` already do this). CAID's dynamic dependency DAG is
largely already shipped as emergent decomposition (ADR 006/007); its remaining half (recompute edges after
each merge) pays off only past single-file Lite work — folded into #13 / future multi-file work.

**Last status:** P1 + P2 shipped (#5–#15); the observability & adoption cluster shipped and merged
(#33–#37, PRs #38–#41 + the live-streaming follow-up) — `aoa` is now OpenTelemetry-native (post-hoc
**and** live), documented for adoption; P3 (#16–#18) deferred with explicit reopen gates. P0 #4
is now complete — the at-scale SWE-bench Lite run achieved a 50% pass rate on a 20-instance subset using the `grok` backend. This single empirical artifact successfully converts the project from "measured architecture" to "measured tool."

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
