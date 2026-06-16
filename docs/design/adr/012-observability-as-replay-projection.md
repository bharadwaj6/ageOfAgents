# ADR 012: Observability is a Replay Projection (OpenTelemetry)

## Status
Accepted

## Context
`aoa` already computes rich run data — tokens, $ cost, merge-queue depth/wait, regression-escape rate,
the MAST failure-mode histogram — but none of it leaves the process. To be usable on real projects the
system needs to be observable: point it at Honeycomb, Grafana Tempo, Datadog, Jaeger, or an OTel
Collector and watch a run as traces and metrics. OpenTelemetry is the vendor-neutral standard for this.

This collides with two non-negotiables. The Event Log is the single source of truth and every view is
derived by *replaying* it (ADR 001); we do not thread bespoke instrumentation through the control loop.
And the default test suite must stay hermetic and offline — no required external service, no network
(ADR 004 / 009). Naive OTel instrumentation would violate both: spans emitted from hot-path call sites,
and an SDK that wants an endpoint at startup.

## Decision
Treat observability as **another replay projection**, exactly like `metrics.Compute` and
`diagnose.Classify`, and keep it **off by default**.

- A new package `internal/otel` exposes `Export(ctx, events, metrics, diagnose, price, extra...)`. It is
  a pure function of a finished Event Log plus the already-computed views; it builds an OTLP
  TracerProvider + MeterProvider, emits, flushes, and shuts down. No orchestrator, ledger, or hot-path
  code changes — the control loop is unaware OTel exists.
- **Traces** are reconstructed from the log into a goal → ticket → attempt span tree, with each event
  riding as a span event and spans backdated to the events' own timestamps. **Metrics** publish the
  `metrics.Metrics` fields and the `diagnose.Report` findings as OTLP gauges under `aoa.*`.
- **Off by default.** `otel.Enabled()` is true only when `OTEL_EXPORTER_OTLP_ENDPOINT` (or a
  signal-specific endpoint) is set; otherwise `Export` returns immediately. The hermetic suite never
  configures an endpoint, so it never opens a socket. Surfaced as `aoa otel export` and `aoa run --otel`.
- **Post-hoc, not live.** The first cut projects the finished log after a run/eval. Live per-append
  streaming (a ledger hook emitting spans in real time) is a deferred follow-up; it would reuse the same
  span model.
- **Vendor-agnostic via standard env vars.** Configuration is the OTel standard
  (`OTEL_EXPORTER_OTLP_ENDPOINT`, `_HEADERS`, `_PROTOCOL`, `OTEL_SERVICE_NAME`). Honeycomb is just an
  endpoint plus an `x-honeycomb-team` header — there is no vendor-specific code in the core.

## Consequences
- The core stays clean: observability is opt-in, isolated, and provably absent from offline runs and the
  test suite (a unit test asserts `Export` is a no-op with the endpoint unset).
- Tradeoff: this relaxes the README's "1 dependency" claim — the OTel SDK + OTLP/HTTP exporters are a new
  dependency cluster. They are isolated in `internal/otel`; the binary still needs **zero external
  services** to run, because OTel only activates when an endpoint is configured.
- Tradeoff: post-hoc export means traces appear after a run completes, not during it. That is sufficient
  for benchmarks and CI; live streaming is tracked separately.
- Because traces/metrics are a projection, they are reproducible from a stored log: the same `events.jsonl`
  re-exports identically, and a new metric or span attribute is added by extending the projection, not by
  re-instrumenting the system.

## Research basis
OpenTelemetry's OTLP + environment-variable specification as the vendor-neutral wire/config standard. The
project's own replay-only discipline (ADR 001) and hermetic-suite rule (ADR 004 / 009), here extended to a
third projection alongside `metrics` and `diagnose`.
