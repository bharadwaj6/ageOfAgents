# Handover — OpenTelemetry observability, cost-sensitive eval, adoption

> Status as of 2026-06-16. This is a living handover doc for the next agent. The originating plan is in
> `~/.claude/plans/here-is-some-feedback-flickering-scott.md`; the decision is recorded in
> [`docs/design/adr/012-observability-as-replay-projection.md`](docs/design/adr/012-observability-as-replay-projection.md).

## The ask (from the user)

> "We should be OpenTelemetry-native and support all the primitives out of the box, working for the
> benchmarks too. The benchmark should be cost-sensitive — run ~20 grok instances instead of ~300 — and
> record what came of those in reports and OTel. Keep it agnostic to Honeycomb but OTel-supporting;
> Honeycomb is the first integration to test. Make the repo usable in real-world new projects — focus on
> integrations and docs. File GitHub issues, then execute. The benchmark is done last, once all of this
> is in place."

## Core design (already decided — do not relitigate)

Observability is **another replay projection** of the Event Log, exactly like `metrics.Compute` and
`diagnose.Classify` — **not** instrumentation threaded through the control loop. `internal/otel.Export`
is a pure function of a finished event slice + the computed views. It is **off by default** (active only
when `OTEL_EXPORTER_OTLP_ENDPOINT` is set) and **post-hoc** (after a run/eval). Traces + metrics only
(events ride as span events; no separate OTel log records). Vendor-agnostic via standard OTLP env vars;
Honeycomb has **zero** special-casing. See ADR 012.

## Status at a glance

| Issue | Title | State |
|-------|-------|-------|
| #33 | OTel replay-to-OTel core (ADR 012) | ✅ Done — PR #38 merged |
| #34 | Honeycomb integration + smoke + docs | ✅ Done — PR #39 merged; **validated live** |
| #35 | Cost-sensitive eval ($ ceiling + per-task accounting + `--otel`) | ✅ Done — PR #40 merged |
| #36 | Adoption docs + examples | ✅ Done — PR #41 merged |
| (n/a) | #4 harness: cost/OTel/grok passthrough | ✅ Done — PR #42 merged |
| (n/a) | gitignore `.env` | ✅ Done — PR #43 merged |
| **#37** | **Live per-append OTel streaming** | ⏸ **Deferred follow-up — open** |
| **#4** | **Run SWE-bench Lite at scale (baseline → README)** | 🔲 **Open — user-driven live run** |

All work is on `main`. No open PRs. `make check` is green (one flaky test — see Gotchas).

---

## DONE — details

### #33 — `internal/otel` replay-to-OTel core (PR #38)
- **`internal/otel/otel.go`** — `Enabled()` (true iff an OTLP endpoint env var is set) and
  `Export(ctx, events, metrics, diagnose, price, extra...)`. Builds an OTLP/HTTP TracerProvider +
  MeterProvider, emits, flushes, shuts down. `ExportTask(...)` is `Export` with a per-task
  `service.name`.
  - **Traces:** goal span → ticket span → attempt span (`WorkStarted` → terminal). Every event is a span
    event; spans are backdated via `trace.WithTimestamp(e.Timestamp)`; failures set span status = Error;
    tokens/model are span attributes.
  - **Metrics:** the `metrics.Metrics` fields + one gauge per `diagnose.Report` finding, under `aoa.*`
    (e.g. `aoa.tokens_total`, `aoa.merged`, `aoa.merge_queue_wait_mean_seconds`, `aoa.cost_usd`,
    `aoa.tokens_by_model{model=…}`, `aoa.failure_mode{mode=…}`).
- **`internal/otel/otel_test.go`** — stands up an in-process OTLP/HTTP receiver, decodes the protobuf,
  and asserts the `goal/ticket/attempt` span tree + that metrics were received. Also asserts `Export` is
  a **no-op** with the endpoint unset (the hermetic guarantee).
- **CLI (`cmd/aoa/main.go`):** `aoa otel export [--path]` and `aoa run --otel`. Added to `usage()`.
- **Deps (`go.mod`):** OTel SDK + OTLP/HTTP exporters (v1.44.0). Isolated to `internal/otel`; the binary
  still needs zero external services. ADR 012 records the relaxed "1 dependency" claim.

### #34 — Honeycomb (PR #39) — **validated live 2026-06-16**
- **`scripts/otel_smoke.sh`** — scaffolds a throwaway workspace, runs the mock orchestrator, then
  `aoa otel export`. Defaults to Honeycomb when `OTEL_EXPORTER_OTLP_ENDPOINT` is unset; reads
  `HONEYCOMB_API_KEY` from env.
- **`docs/integrations/honeycomb.md`** — the four env vars + "it's just OTLP → Tempo/Datadog/Jaeger/
  Collector".
- **Validation result:** ran against the user's key (loaded from local `.env`). The key resolved (via
  `https://api.honeycomb.io/1/auth`) to team `bharadwaj`, environment `ageOfAgents`, `events: true`; the
  OTLP flush returned no error (2xx). Data should be visible in Honeycomb env `ageOfAgents`, service
  `aoa`. The key has `queries: false`, so spans cannot be read back via API — UI only.

### #35 — Cost-sensitive eval (PR #40)
- **`cmd/aoa/main.go` `cmdEval`:** new flags `--price-file` (TOML `[pricing]` map, per-model $),
  `--max-cost` ($ ceiling — stops launching tasks **between** tasks once cumulative spend crosses it;
  footer reports ran vs skipped), `--otel` (exports each task's log as its own OTLP trace/service).
  Helper `evalCost(metrics, priceMap, flat)`; `printEvalTable` gained a per-task `$` column + aggregate
  footer (`solved=k/n  tokens=…  cost=$…  (ran R/T, skipped S by --max-cost)`).
- **`internal/config/config.go`:** `LoadPricing(path)` reads a standalone `[pricing]` TOML.
- **`internal/otel/otel.go`:** `ExportTask` (per-task `service.name`).
- **Tests (`cmd/aoa/cost_test.go`):** `evalCost` (per-model / flat / unpriced) and the `printEvalTable`
  footer (totals + "skipped N by --max-cost") via stdout capture.
- **Note:** `--json` output is unchanged (per-task `tokens`/`tokens_by_model` already present ⇒ $ is
  derivable). Cost columns/footer live in the human table.

### #36 — Adoption docs (PR #41)
- `docs/config-reference.md` (every `aoa.toml` field, defaults, when-to-set), `docs/integrations/README.md`
  (OTel + backends index), `examples/README.md` + `examples/sample-aoa.toml` (worked adopt-your-repo
  runbook). README: `otel`/`eval` flags in the Commands table, an observability receipt bullet, links.
- **Deliberately skipped** a separate `docs/quickstart.md` — the README is already the quickstart.

### #42 — SWE-bench harness wiring
- `scripts/eval_swebench.sh` + `scripts/eval_swebench_docker.sh` honor optional `MAX_COST`,
  `PRICE_FILE`, `OTEL` env vars (passed to `aoa eval`) and document `BACKEND=grok`.
- `scripts/swebench_to_tasks.py` caches each repo clone under `~/.cache/aoa/swebench_repos` and cuts
  per-instance with a fast `--local` clone.
- `docs/design/roadmap.md` updated (observability & adoption cluster recorded; #4 marked
  cost-capped + OTel-ready).

---

## REMAINING

### 🔲 #4 — Run SWE-bench Lite at scale (the last, user-driven step)

This is an **empirical live run**, not a coding task. The harness is fully ready and cost-capped. What it
needs (none of which the previous agent had access to):
1. **A Grok API key** in the environment (the `grok` backend uses it).
2. **The SWE-bench Lite dataset** as JSON. `scripts/swebench_lite.json` in the repo is an **empty
   placeholder** (it's gitignored). Pull the `princeton-nlp/SWE-bench_Lite` split from HuggingFace and
   export to a JSON array/JSONL.
3. **Docker** for *scored* verification — use `scripts/eval_swebench_docker.sh` (3-phase: aoa generates
   patches in inference-mode → extract diffs → official `swebench.harness.run_evaluation` scores them).
   ADR 009: aoa orchestrates + verifies, but does **not** provision the per-repo test env.

**The cost-capped, traced command (20 instances, $10 ceiling, → Honeycomb):**
```sh
MAX_COST=10 PRICE_FILE=examples/sample-aoa.toml OTEL=1 \
  OTEL_EXPORTER_OTLP_ENDPOINT=https://api.honeycomb.io \
  OTEL_EXPORTER_OTLP_HEADERS="x-honeycomb-team=$HONEYCOMB_API_KEY" \
  scripts/eval_swebench.sh swebench_lite.json grok 20
```
For real scored numbers use `scripts/eval_swebench_docker.sh swebench_lite.json grok 20` (same env vars
apply to its inference phase).

**Then:** put pass@1 / cost-per-solve / tokens-per-solve / MAST into the README (replace the placeholder
blockquote under the intro). Update `docs/design/roadmap.md` P0 and close #4. Verify the grok per-model
price in `examples/sample-aoa.toml` (`grok = 5.0`) matches real pricing before trusting the `$` numbers.

**Before trusting the `$` figures:** confirm the `grok` backend actually populates `Result.Model` and
`Result.Tokens` (check `internal/agent/grok.go`). If `Model` is `""`, per-model pricing via
`--price-file` won't match; fall back to a flat `--price` or fix the backend to report its model id.

### ⏸ #37 — Live per-append OTel streaming (deferred)
Open as a follow-up. Stream each appended `api.Event` to a live span as it happens (a ledger/orchestrator
seam) so traces appear **during** `aoa run`, not just post-hoc. Must stay off-by-default and never
networked in the hermetic suite. Reuse the span model in `internal/otel`. Only build if live traces are
actually wanted — post-hoc replay already covers benchmarks/CI.

---

## How to work in this repo (gotchas the next agent must know)

- **Protected `main` + "Gas Town HQ" hook.** Direct pushes to `main` are blocked and a global
  post-checkout hook reverts the town root to `main`. **Always work in a `git worktree`** off `main`,
  commit there, push the branch, open a PR, merge via `gh pr merge --rebase`. (See memory
  `aoa-git-workflow`.)
- **Stacked PRs + rebase merge.** When PR B is stacked on branch A and A is **rebase-merged** to main, A's
  commit gets a new SHA, so B will show CONFLICTING. Fix: in B's worktree, `git fetch origin && git rebase
  origin/main` (the original A commit is auto-skipped as an already-applied patch), `make check`, then
  `git push --force-with-lease`, then merge B. This happened with PRs #39/#40 here.
- **`gh pr merge --delete-branch` fails to delete the local branch** while a worktree still uses it
  (harmless). Clean up after: `git worktree remove ../<dir> --force && git branch -D <branch>`.
- **`make check` has a flaky test:** `internal/worktree` intermittently fails under parallel load (macOS
  APFS `RemoveAll` race; mitigated with `gc.auto=0` but not eliminated). It **passes on isolated
  re-run** (`go test ./internal/worktree/`). Re-run before assuming a real break. Don't "fix" it by
  patching in a loop.
- **gopls is confused inside worktrees** ("not included in your workspace" / "use of internal package not
  allowed"). These diagnostics are **noise** — trust `go build ./...` and `make check`.
- **`ruff` is not installed locally.** Python changes (the swebench scripts) were hand-cleaned. Install
  `ruff` (`uv tool install ruff`) if touching Python, per the user's global rules.
- **Commits:** Conventional Commits, ≤72-char subject, **no AI model names** in subject/body (only the
  `Co-Authored-By:` trailer). Run `make check` green before every PR.
- **Secrets:** `.env` is gitignored (PR #43). The Honeycomb key lives in local `.env` as
  `HONEYCOMB_API_KEY=…`; load with `set -a && . ./.env && set +a`.

## Project non-negotiables (from CLAUDE.md / AGENTS.md)
Event Log is the single source of truth (state by replay); nothing merges unless the Gate passes; one
deterministic Scheduler (no LLM in the control plane); all LLM access via `agent.Backend`; no
markets/voting/consensus; the test suite stays hermetic/offline (the `mock` backend never networks).
Observability honors this by being a pure, off-by-default replay projection.

## Key references
- Plan: `~/.claude/plans/here-is-some-feedback-flickering-scott.md`
- ADR: `docs/design/adr/012-observability-as-replay-projection.md`
- Roadmap: `docs/design/roadmap.md` (Phase E)
- Integration docs: `docs/integrations/`, config: `docs/config-reference.md`, examples: `examples/`
