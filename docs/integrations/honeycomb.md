# Honeycomb (and any OTLP backend)

`aoa` is OpenTelemetry-native: it projects a finished Event Log into OTLP **traces** (a
goal → ticket → attempt span tree) and **metrics** (`aoa.*` gauges for tokens, cost, merge-queue
depth/wait, regression escapes, the MAST failure-mode histogram). It is **off by default** and
**vendor-agnostic** — there is no Honeycomb-specific code. Honeycomb is just an OTLP endpoint plus an
auth header. See [ADR 012](../design/adr/012-observability-as-replay-projection.md).

## Configure

Set the four standard OpenTelemetry environment variables:

```sh
export OTEL_EXPORTER_OTLP_ENDPOINT="https://api.honeycomb.io"
export OTEL_EXPORTER_OTLP_HEADERS="x-honeycomb-team=YOUR_HONEYCOMB_API_KEY"
export OTEL_SERVICE_NAME="aoa"            # the service/dataset name in Honeycomb
# optional: export OTEL_EXPORTER_OTLP_PROTOCOL="http/protobuf"  (the default)
```

Then export after any run:

```sh
aoa run --otel              # reconcile, then export
# or, against an existing workspace (e.g. after a bench/eval):
aoa otel export
```

With no endpoint configured, `aoa run --otel` is a no-op and `aoa otel export` exits with a clear
message — offline runs and the hermetic test suite never touch the network.

## One-command smoke test

```sh
HONEYCOMB_API_KEY=hcaik_... scripts/otel_smoke.sh
```

This scaffolds a throwaway workspace, runs the mock orchestrator, and ships the result to Honeycomb.

## What you'll see

- **Traces:** one trace per goal. The root `goal <id>` span contains a `ticket <id>` span per unit of
  work, each containing an `attempt N` span (`WorkStarted` → `Merged`/`VerificationFailed`). Every log
  event appears as a span event; failures set the span status to error. Spans are backdated to the
  events' real timestamps, so durations reflect the actual run.
- **Metrics:** gauges named `aoa.tokens_total`, `aoa.merged`, `aoa.failed`, `aoa.merge_queue_max_depth`,
  `aoa.merge_queue_wait_mean_seconds`, `aoa.regression_escape_rate`, `aoa.cost_usd` (when `[pricing]` is
  set), `aoa.tokens_by_model{model=...}`, and `aoa.failure_mode{mode=...}` (the MAST histogram).

## Not Honeycomb?

It's just OTLP. The same variables point at anything that speaks OTLP/HTTP:

```sh
# Grafana Tempo / Alloy, Datadog agent, Jaeger, or an OpenTelemetry Collector
export OTEL_EXPORTER_OTLP_ENDPOINT="http://localhost:4318"
# add OTEL_EXPORTER_OTLP_HEADERS for whatever auth your backend wants
aoa otel export
```

A local OTel Collector (or `otel-tui`) on `:4318` is the easiest way to eyeball the output without a
vendor account.
