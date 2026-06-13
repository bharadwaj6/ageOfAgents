# Age of Agents v2 — Architecture

A minimal, **verifier-gated** orchestrator for fleets of AI coding agents. It keeps the small set of
distributed-systems primitives that map to real, measured failure modes, and deletes the
anthropomorphic and game-theoretic machinery that the research shows is the wrong tool for *aligned*
coding agents.

> **Thesis update.** The bottleneck in multi-agent coding is **verification + specification +
> idempotency**, not hierarchy, markets, or multi-agent debate. So we invest there and nowhere else.

## 1. Why v2 (and what changed)

The earlier `aoa` implementation (`main`, ~9.7k LOC) went all-in on the original distributed-systems +
game-theory vision: a Contract Net Protocol market, β-horizon/council consensus, four-dimensional trust,
ACO pheromones, Dolt/beads, OTEL, and Gas Town migration. It works, but it is large and invests heavily
in mechanisms that recent empirical work indicates are counterproductive for aligned coding agents.

The research corpus (`docs/claude.md`, `docs/gemini.md`, `docs/grok.md`, `docs/perplexity.md`,
`docs/research_links.md`) converges on a sharper picture:

- **Most multi-agent failures are coordination/design/verification failures, not model failures.** The
  MAST taxonomy (Cemri et al., *Why Do Multi-Agent LLM Systems Fail?*, NeurIPS 2025; 1,642 traces;
  41–86.7% failure rates) attributes failures to **System Design 44.2%**, **Inter-Agent Misalignment
  32.3%**, **Task Verification 23.5%**. The single largest individual mode is **Step Repetition 15.7%**
  — an idempotency/state problem. Adding objective verification yields ~**+15.6%** success.
- **Consensus voting has a "popularity trap."** Vallecillos-Ruiz, Hort & Moonen (arXiv:2510.21513): a
  diversity-based selector reaches ~95% of the ensemble's potential while consensus selection "amplifies
  common but incorrect outputs." Majority voting is the wrong selector for code.
- **Multi-agent debate ≈ self-consistency at equal cost** (Huang et al., ICLR 2024, arXiv:2310.01798).
  Self-correction without an external signal is unreliable; an **objective verifier (tests)** is what
  actually works.
- **Multi-agent is weak for interdependent tasks** and costs ~15× the tokens of a single agent
  (Anthropic, *How we built our multi-agent research system*). Coding is highly interdependent — so
  parallelism must be paired with hard gates and good decomposition, not raw agent count.
- **Coordinate via shared state, not direct messaging.** Stigmergy's actionable lesson for LLM agents is
  the **blackboard** model (read/write shared state); practitioners report ~80% token savings vs.
  agent-to-agent chat (`docs/grok.md`). We adopt the blackboard, not literal pheromone simulation.

An independent critique of this plan (`docs/gemini.md`) validated these decisions — and concluded the
game-theoretic primitives are "overkill and often detrimental" for software development — while flagging
two scalability caveats we address in §5.

## 2. Design principles

1. **Event-sourced truth.** An append-only log is the single source of truth; all state is a fold of
   events. Crash recovery, audit, and replay come for free.
2. **Objective verification gates everything.** Nothing reaches `main` unless a real verifier (build /
   tests / lint) passes. Correctness is checkable for code — lean on that, not on agent opinions.
3. **Idempotency by construction.** Every unit of work carries an idempotency key; re-running a
   completed step is a no-op. Directly attacks the #1 failure mode.
4. **Deterministic coordination, stochastic execution.** Coordination is plain Go code (a reconciler),
   not an LLM. Only the *work* is done by stochastic agents.
5. **Coordinate through shared state.** Agents never message each other; they read/write the ledger.
6. **Keep the substrate boring and portable.** One static binary, plain JSONL, one config file, git
   only. No databases, no brokers, no required external services.

## 3. The model

```
            goal
             │
             ▼
        ┌─────────┐   append    ┌──────────────────┐
        │  Ledger │◀────────────│   Reconciler      │  (deterministic, ms)
        │ (JSONL) │────────────▶│  observe→fold→act │
        └─────────┘   replay    └──────────────────┘
             ▲                      │ dispatch          │ drive
             │                      ▼                   ▼
             │               ┌────────────┐      ┌───────────────┐
             │   proposals   │  Workers    │      │  Merge queue   │
             └───────────────│  (agents in │      │  verify→merge  │──▶ main
                             │  worktrees) │      │  (serialized)  │
                             └────────────┘      └───────────────┘
```

**Domain objects**

- **Goal** — a human-submitted objective. Decomposed into tickets (initially, and emergently at runtime).
- **Ticket** — an atomic unit of work with an idempotency key and `depends_on` edges. Only
  dependency-ready tickets are dispatchable. Workers may append new tickets (emergent decomposition).
- **Worker** — an agent session that executes one ticket in an isolated git worktree and returns a
  **proposal** (a branch/commit + a short reasoning trace).
- **Proposal** — a candidate change submitted to the merge queue.
- **Verifier** — configured commands (e.g. `go build`, `go test`, `golangci-lint`) whose exit status
  gates the merge.

**The reconciler loop** (`internal/orchestrator`): `observe(ledger) → fold to state → diff desired vs
actual → act (dispatch ready tickets under the concurrency governor; run failure detector; drive merge
queue) → append resulting events → repeat`. One controller, not eleven.

## 4. Components (Go packages)

| Package | Responsibility |
|---------|----------------|
| `pkg/api` | Event envelope + the small event set (see §6). |
| `internal/ledger` | Append-only JSONL log: `Append`, `Read`, `Replay`. |
| `internal/state` | Pure fold of events → `State` (tickets, deps, workers, merge queue). |
| `internal/orchestrator` | The single reconciler: dispatch + governor + failure detector + merge-queue driver. |
| `internal/agent` | `Backend` interface (LLM-provider abstraction) + `mock` and `claudecode` backends. |
| `internal/worktree` | Git worktree provisioning / cleanup for isolated worker sandboxes. |
| `internal/verify` | Run configured verification commands; capture pass/fail + output. |
| `internal/mergequeue` | Serialize proposals → verify → merge to `main` or reject; emit events. |
| `internal/config` | One TOML config: repo path, verify commands, concurrency, backend, conventions. |
| `cmd/aoa` | Tiny standard-library CLI (no framework): `init`, `goal`, `run`, `status`, `feed`, `events`. |

The **`agent.Backend`** interface is the only seam to the LLM. Business logic never calls a provider SDK
directly. A deterministic **`mock`** backend lets the entire loop run offline in `go test`; the
**`claudecode`** backend drives a real agent as a subprocess in the ticket's worktree.

## 5. Two scalability refinements (from the gemini critique)

1. **No central-planner bottleneck.** The reconciler is deterministic code (sub-millisecond), so it is
   not a cognitive bottleneck the way Gas Town's Mayor LLM was. We further avoid a rigid up-front plan:
   a worker can emit `TicketCreated` to extend the shared graph at runtime (LATTE-style emergent task
   graph) — coordination via the blackboard, no auctioneer.
2. **The merge queue is not a global barrier.** It serializes only *writes to `main`* (required for
   linearizability/correctness); it does **not** block worker dispatch — workers keep claiming ready
   tickets while the queue drains. *Future optimization (not in the MVP):* verify/merge non-conflicting
   proposals (disjoint file sets) in parallel batches.

## 6. Events

A single envelope `{seq, type, ts, actor, payload}` over an append-only JSONL log. The v1 event set is
deliberately small:

`GoalSubmitted` · `TicketCreated` · `TicketReady` · `TicketClaimed` · `WorkStarted` · `Heartbeat` ·
`ProposalSubmitted` · `VerificationPassed` · `VerificationFailed` · `Merged` · `TicketFailed` ·
`WorkerStalled` · `WorkerRestarted`.

State (tickets, dependency readiness, worker status, the merge queue) is a pure fold over this stream;
there is no separate mutable store to keep consistent.

## 7. What we deliberately do NOT build

| Rejected | Why (research) |
|----------|----------------|
| CNP market + strategic bidding | Markets assume self-interested agents; aligned coding agents are a *pure coordination* problem. Keep only capability routing if ever needed — no bidding. |
| ACO pheromone simulation | Only pays under task locality; pollution/convergence/debugging costs; unnecessary given an objective verifier. We keep the *blackboard* form of stigmergy, not pheromones. |
| β-horizon / council multi-agent consensus | Debate ≈ self-consistency at equal cost; voting hits the popularity trap. Objective verifier instead. |
| Four-dimensional trust registry | We control the agents — instrument, don't incentivize. A simple pass-rate can be added later if needed. |
| Dolt CQRS, `bd`/beads adapter | Extra moving parts; the JSONL ledger already gives queryable, replayable truth. |
| Multi-tier escalation, federation, gossip, leader election | Single-node MVP; one failure detector + crash-only restart covers recovery. Defer until genuinely multi-node. |

These are revisitable, but each must earn its place against a measured failure mode.

## 8. How the four goals are met

- **Set up:** `go build` → one binary; `aoa init` scaffolds a repo + `aoa.toml`; zero required external
  services (git only).
- **Use:** `aoa goal "…"` then `aoa run` drives goal → decompose → dispatch agents → verify → merge,
  with a live `aoa feed`.
- **Validate:** the deterministic mock backend runs the whole loop in `go test` with no network; the
  verifier gate is the correctness mechanism; the event log replays for debugging (eval-first).
- **Port:** static Go binary, plain JSONL events, no DB, one config file.

## 9. References

How we measure whether this design delivers: [`docs/v2/metrics.md`](metrics.md) (eval-first metrics +
litmus test). See `docs/research_links.md` for the full source list. Load-bearing citations: Cemri et al.
(arXiv:2503.13657, MAST); Vallecillos-Ruiz et al. (arXiv:2510.21513, ensemble popularity trap); Huang
et al. (arXiv:2310.01798, debate ≈ self-consistency); Anthropic multi-agent research engineering post;
LATTE (emergent task graphs); stigmergy/blackboard synthesis (`docs/grok.md`).
