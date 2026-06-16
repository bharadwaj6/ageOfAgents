# Integrations

`aoa` is one binary that needs only git — no required external service. Everything below is **opt-in**:
unconfigured, it stays fully offline (and the test suite never networks).

## Observability (OpenTelemetry)

`aoa` is OpenTelemetry-native: it projects a finished Event Log into OTLP **traces** (a goal → ticket →
attempt span tree) and **metrics** (`aoa.*` gauges — tokens, `$`, merge-queue depth/wait, regression
escapes, the MAST failure-mode histogram). It is **off by default** and **vendor-agnostic** — configured
entirely through the standard OTel environment variables, so any OTLP backend works. See
[ADR 012](../design/adr/012-observability-as-replay-projection.md).

```sh
export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4318"   # your Collector / vendor endpoint
export OTEL_SERVICE_NAME="aoa"
aoa run --otel        # reconcile, then export (post-hoc)
aoa run --otel-live   # stream spans as events happen, during the run
aoa otel export       # or export an existing workspace's log on demand
```

`--otel` exports once after the run; `--otel-live` streams the goal → ticket → attempt tree as it
unfolds (it also backfills spans for any work already in flight). With no endpoint set, both are no-ops
and `aoa otel export` exits with a clear message.

| Backend | How |
|---------|-----|
| **Local Jaeger (Docker)** | Start our local sandbox: `cd examples/observability && docker-compose up -d`. Then export using `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318`. View traces at [http://localhost:16686](http://localhost:16686). |
| **[Honeycomb](honeycomb.md)** | `OTEL_EXPORTER_OTLP_ENDPOINT=https://api.honeycomb.io` + `OTEL_EXPORTER_OTLP_HEADERS=x-honeycomb-team=<key>`. One-command smoke test: `scripts/otel_smoke.sh`. |
| **Grafana Tempo / Alloy, Datadog, Jaeger, OTel Collector** | Same env vars, point `OTEL_EXPORTER_OTLP_ENDPOINT` at the collector (`:4318` for OTLP/HTTP) and add whatever auth header it wants. A local Collector or `otel-tui` is the easiest way to eyeball output. |

## Agent backends

The `backend` field in `aoa.toml` selects the AI engine Workers use. All LLM access goes through one
interface (`agent.Backend`, ADR 004); the control plane is identical across backends.

| Backend | Needs | Use for |
|---------|-------|---------|
| `mock` | nothing (offline, deterministic) | trying `aoa` out, the hermetic test suite, CI |
| `claudecode` | the `claude` CLI authenticated; network + API cost | real coding work |
| `grok` | a Grok API key in the environment; network + API cost | real coding work / benchmarking |
| `openai` | `OPENAI_API_KEY` in environment; network + API cost | real coding work natively with OpenAI |

### Custom Plugins (OpenRouter, DeepSeek, Together AI)

You can define custom backends in `aoa.toml` that point to any OpenAI-compatible API using the `openai_compatible` plugin type:

```toml
backend = "my_openrouter"

[backends.my_openrouter]
type = "openai_compatible"
base_url = "https://openrouter.ai/api/v1/chat/completions"
model = "anthropic/claude-3.5-sonnet"
api_key_env = "OPENROUTER_API_KEY"
```

Once defined, set the `OPENROUTER_API_KEY` in your environment, and `aoa` will route all tasks through OpenRouter natively.

Cost is purely a property of the backend you choose. Token/`$` accounting flows through the Event Log;
set `[pricing]` in `aoa.toml` (USD per million tokens, by model) to turn token counts into `$` in
`aoa status`, `aoa eval`, and the OTel `aoa.cost_usd` metric. See the
[configuration reference](../config-reference.md).
