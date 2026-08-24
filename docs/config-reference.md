# Configuration reference — `aoa.toml`

One TOML file per workspace (next to the `.aoa/` Event Log). `aoa init` writes a sensible default; every
field below is optional and falls back to the default shown. Unset governors are **off**, so a fresh
config behaves exactly like the hermetic suite until you opt in.

## Core

| Field | Type | Default | What it does |
|-------|------|---------|--------------|
| `repo` | string | `./repo` | Path to the integration git repo (relative to the workspace root, or absolute). `aoa init --adopt` sets this to your existing repo. |
| `backend` | string | `"mock"` | Which coding agent to drive: `mock` (offline fixture) · `claudecode` · `codex` · `cursor` · `gemini` · `grok` (CLI harnesses) · `openai` · `anthropic` (direct APIs) · a plugin named in `[backends]`. See [harnesses](harnesses/README.md). |
| `concurrency` | int | `4` | Max Workers in flight (the Concurrency Limit). |
| `max_attempts` | int | `2` | How many times a Task is retried before it fails. |
| `best_of_n` | int | `1` | Concurrent attempts dispatched per Task (parallel generation). Each attempt consumes a concurrency slot, and the Gate — never a vote — picks the winner (ADR 002). |
| `conventions_file` | string | — | A file whose contents are injected into every agent prompt as shared coding rules. |
| `sandbox` | string | `""` (host) | How the Gate's commands are isolated. `"docker"` runs each verify command in a container; `""` runs them on the host. |
| `sandbox_image` | string | `"golang:1.26"` | Container image used when `sandbox = "docker"`. The default carries only a Go toolchain — set a prepared image when the Gate needs another language's dependencies. |

## Backends

| Field | Type | Default | What it does |
|-------|------|---------|--------------|
| `fallback_backends` | list of string | `[]` | Ordered backend ids to try when the primary Backend returns an error (rate limits, API failures). The first success wins. |
| `[backends.<name>]` | table | — | Defines a backend plugin usable as `backend = "<name>"`. A block here **shadows a built-in of the same name**, which is how a preset's flags get corrected without waiting for a release. |
| `[backends.<name>].type` | string | — | `"openai_compatible"` for an OpenAI-shaped HTTP endpoint, or `"cli"` to drive any coding-agent CLI ([BYOHarness](harnesses/byo-cli.md)). |
| `[backends.<name>].base_url` · `.model` · `.api_key_env` | string | — | For `type = "openai_compatible"`. |
| `[backends.<name>].bin` | string | — | For `type = "cli"`: the binary to invoke. Required. |
| `[backends.<name>].args` | list of string | `[]` | For `type = "cli"`: passed verbatim, then the prompt is appended as the final argument, as a single argv element (no shell). |

Drive a CLI aoa has no preset for:

```toml
backend = "mycoder"

[backends.mycoder]
type = "cli"
bin  = "mycoder"
args = ["run", "--yes"]     # -> mycoder run --yes "<prompt>"
```

```toml
backend           = "openrouter"
fallback_backends = ["anthropic"]

[backends.openrouter]
type        = "openai_compatible"
base_url    = "https://openrouter.ai/api/v1"
model       = "anthropic/claude-opus-4.1"
api_key_env = "OPENROUTER_API_KEY"
```

Because `base_url` is per-backend, pointing `aoa` at an LLM proxy or gateway needs no code — set it to the
gateway's endpoint and the gateway's own budgets stack on top of the in-process governors below.

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
| `max_usd_per_goal` | float | `0` (unlimited) | Per-Goal **dollar** budget. Requires `[pricing]` — with no price for the models in use the spend is `$0` and this never trips. | You'd rather reason in money than tokens. |
| `retry_backoff` | duration string | `"0s"` (instant) | Base wait before re-dispatching a failed Task; grows exponentially per attempt. | A flaky Gate/agent is hammering retries. |
| `crash_loop_threshold` | int | `3` | Give up on a Task after N **identical-reason** failures in a row, even under `max_attempts`. Inert while `≤ max_attempts`. | Distinguishing a flaky failure from a fundamentally-stuck one. |

Both budgets count **every** attempt, including ones that errored, produced no changes, or were rejected
by the Gate — the failure spiral is exactly where an unattended run burns money without shipping anything,
so it is what the breaker is there to bound.

## Termination gates

The bounds that stop an unattended run from going forever. Each is already enforced with the default
shown; setting it here only changes the value. Leave them alone unless a real run hits one.

| Field | Type | Default | What it does | Set it when |
|-------|------|---------|--------------|-------------|
| `poll_interval` | duration string | `"100ms"` | How long `aoa run` waits between reconcile passes while workers are still busy. Dispatch is asynchronous (ADR 013), so the loop polls; this trades responsiveness against wakeups. Rarely worth changing. | You are running very many short tasks and want tighter merge latency. |
| `agent_timeout` | duration string | `"30m"` | Hard ceiling on a **single agent attempt**. Distinct from `stall_timeout`: a running Worker heartbeats, so that setting bounds *silence* while this bounds *runtime*. It is what stops a wedged agent CLI hanging the run forever, so keep it comfortably above how long a real task takes. | Your tasks are much larger or much smaller than the 30m default. |
| `stall_timeout` | duration string | `"2m"` | How long a Worker may go without progress before the Stall Detector restarts it. Workers emit a `Heartbeat` while running, so this bounds *dead* work, not merely slow work. | Your agent legitimately runs long and a crashed process should still be reaped promptly. |
| `max_passes` | int | `1000` | Hard cap on reconcile passes in one `aoa run`. | A pathological graph is churning and you want it to give up sooner. |
| `max_graph_depth` | int | `5` | How deep emergent decomposition may nest (graph governor, ADR 007). | Agents are over-decomposing instead of writing code. |
| `max_tickets_per_goal` | int | `64` | Total Tasks one Goal may spawn. | Bounding blast radius on a vague Goal. |
| `max_fan_out` | int | `8` | New children a single decomposition may emit. | One agent keeps proposing sprawling task lists. |

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
