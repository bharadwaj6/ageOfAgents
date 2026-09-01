---
name: aoa-events-and-state
description: Use when adding or changing anything the aoa Event Log carries — a new lifecycle event type, a field of replayed state, a run metric, or an OpenTelemetry span. Covers the pkg/api → state.Apply → orchestrator → test sequence, the token-accounting trap, and why observability is a replay projection.
---

# Events, state and metrics

The Event Log is the single source of truth: all state is a replay of `pkg/api` events (ADR 001). New
behaviour is **a new event type plus a replay case**, never a side table, a cache, or a field mutated in
place.

## Adding a lifecycle event

1. **`pkg/api/events.go`** — add the event-type constant and its typed payload struct.
2. **`internal/state/state.go:Apply`** — add the case that folds it into state. Pure function, no I/O.
3. **`internal/orchestrator`** — emit it where the transition actually happens, appending through the
   ledger so it is durable before anything acts on it.
4. **Test it.** Round-trip the payload, and assert the state a replay produces — a test that never
   exercises the new case has shipped here before.

Events are append-only and replayed by old and new binaries alike: add fields, don't repurpose them, and
don't change what an existing event means.

## Token usage: charge it in both places

If an event carries token usage, account for it in **`internal/state` and `internal/metrics`**. They read
the same log through different projections and have drifted apart before — the governor and `aoa status`
once disagreed by 50% because only one of them was updated. Change both, and cover both with a test.

## Adding a metric or a span

Observability is a replay projection (ADR 012), not instrumentation:

- A new number → extend the projection in `internal/metrics` (or `internal/diagnose` for a failure mode).
- A trace/metric export → extend `internal/otel`, behind `otel.Enabled()` so offline runs and tests stay
  silent. `Enabled()` is driven by the `OTEL_EXPORTER_OTLP_*` endpoint environment variables.

**Never put a span or metric call into the orchestrator, the ledger, or the merge queue.** The control
loop stays free of instrumentation; anything you want to see must be derivable from the log by replay. If
it is not derivable, the missing piece is an event, not a counter.

Tests must never network — the OTLP path is exercised against a local fake, and `scripts/otel_smoke.sh`
covers the live one outside `make check`.
