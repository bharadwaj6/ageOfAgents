# Configuration reference — `aoa.toml`

One TOML file per workspace (next to the `.aoa/` Event Log). `aoa init` writes a sensible default; every
field below is optional and falls back to the default shown. Unset governors are **off**, so a fresh
config behaves exactly like the hermetic suite until you opt in.

## Core

| Field | Type | Default | What it does |
|-------|------|---------|--------------|
| `repo` | string | `./repo` | Path to the integration git repo (relative to the workspace root, or absolute). `aoa init --adopt` sets this to your existing repo. |
| `backend` | string | `"mock"` | AI engine: `mock` (offline) · `claudecode` · `grok`. See [integrations](integrations/README.md). |
| `concurrency` | int | `4` | Max Workers in flight (the Concurrency Limit). |
| `max_attempts` | int | `2` | How many times a Task is retried before it fails. |
| `conventions_file` | string | — | A file whose contents are injected into every agent prompt as shared coding rules. |

## The Gate

| Field | Type | Default | What it does |
|-------|------|---------|--------------|
| `verify` | list of argv | `go build` + `go test` | **The Gate.** Ordered commands every proposal must pass before it can merge. Nothing merges unless this is green (ADR 002). |
| `regression_verify` | list of argv | `[]` (off) | A broader test set run against post-merge `main` after a proposal passes the Gate. **Never blocks a merge** — it measures the regression-escape rate (the Gate's blind spot; see [metrics](design/metrics.md)). |
| `require_approval` | bool | `false` | Park every Gate-verified proposal for a human decision (`aoa approve` / `aoa reject`) before it merges (ADR 008). |

## Cost & safety governors

All default to off/unlimited, so they never change behavior until set.

| Field | Type | Default | What it does | Set it when |
|-------|------|---------|--------------|-------------|
| `max_tokens_per_goal` | int | `0` (unlimited) | Per-Goal token budget; the spend governor stops dispatching a Goal's remaining work once it's crossed (circuit breaker). | Pointing a real backend at an open-ended Goal and you want a hard ceiling. |
| `retry_backoff` | duration string | `"0s"` (instant) | Base wait before re-dispatching a failed Task; grows exponentially per attempt. | A flaky Gate/agent is hammering retries. |
| `crash_loop_threshold` | int | `3` | Give up on a Task after N **identical-reason** failures in a row, even under `max_attempts`. Inert while `≤ max_attempts`. | Distinguishing a flaky failure from a fundamentally-stuck one. |

## Pricing

```toml
[pricing]            # USD per *million* tokens, keyed by the model id the backend reports
claudecode = 15.0
grok       = 5.0
```

Absent ⇒ unpriced (`$0`). Token counts stay the source of truth; `$` is `tokens × price` applied only at
the reporting edge — surfaced in `aoa status`, `aoa eval` (per-task `$` + `--max-cost` ceiling), and the
OTel `aoa.cost_usd` / `aoa.tokens_by_model` metrics. `aoa eval --price-file` accepts the same `[pricing]`
table as a standalone file.

## Example

A worked config is in [`examples/`](../examples/). To regenerate the default, run `aoa init`.
