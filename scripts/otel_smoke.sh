#!/usr/bin/env bash
# Smoke-test the OTLP export end to end against a real backend.
#
#   HONEYCOMB_API_KEY=hcaik_... scripts/otel_smoke.sh
#
# Runs the hermetic bench into a throwaway workspace, then `aoa otel export`
# ships its Event Log to the configured OTLP endpoint as traces + metrics.
#
# Honeycomb is just one OTLP endpoint. To point at anything else (Grafana Tempo,
# Datadog, Jaeger, an OTel Collector), set OTEL_EXPORTER_OTLP_ENDPOINT /
# OTEL_EXPORTER_OTLP_HEADERS yourself and skip the Honeycomb defaults below.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# Default to Honeycomb when no endpoint is set explicitly.
if [ -z "${OTEL_EXPORTER_OTLP_ENDPOINT:-}" ]; then
  : "${HONEYCOMB_API_KEY:?set HONEYCOMB_API_KEY (or set OTEL_EXPORTER_OTLP_ENDPOINT yourself)}"
  export OTEL_EXPORTER_OTLP_ENDPOINT="https://api.honeycomb.io"
  export OTEL_EXPORTER_OTLP_HEADERS="x-honeycomb-team=${HONEYCOMB_API_KEY}"
fi
export OTEL_SERVICE_NAME="${OTEL_SERVICE_NAME:-aoa}"

echo "Building aoa..."
go build -o aoa ./cmd/aoa

WORK="$(mktemp -d -t aoa-otel-smoke.XXXXXX)"
trap 'rm -rf "$WORK"' EXIT
echo "Workspace: $WORK"

# A tiny real run: scaffold a workspace, submit a goal, reconcile once. The mock
# backend keeps it offline up to the export step.
"$ROOT/aoa" init --path "$WORK" >/dev/null
"$ROOT/aoa" goal --path "$WORK" "add a greeting function" >/dev/null
"$ROOT/aoa" run  --path "$WORK" >/dev/null

echo ""
echo "=== Exporting Event Log to OTLP ($OTEL_EXPORTER_OTLP_ENDPOINT) ==="
"$ROOT/aoa" otel export --path "$WORK"

echo ""
echo "Done. Look for service '$OTEL_SERVICE_NAME' in your backend:"
echo "  traces:  goal -> ticket -> attempt spans"
echo "  metrics: aoa.tokens_total, aoa.merged, aoa.merge_queue_*, aoa.failure_mode, ..."
