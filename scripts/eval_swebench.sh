#!/usr/bin/env bash
# Run aoa end-to-end on SWE-bench (Lite) tasks with the real claudecode backend.
#
#   scripts/eval_swebench.sh INSTANCES.json [BACKEND] [LIMIT]
#
#   INSTANCES.json  SWE-bench instances (JSON array or JSONL). Pull the Lite
#                   split from HuggingFace (princeton-nlp/SWE-bench_Lite) and
#                   export it to JSON; this script never touches the network for
#                   the dataset itself.
#   BACKEND         mock | claudecode | grok   (default: claudecode)
#   LIMIT           only the first N instances (default: all)
#
# Cost-aware / observability env vars (all optional) — the cost-sensitive run:
#   LIMIT=20 BACKEND=grok MAX_COST=10 PRICE_FILE=examples/sample-aoa.toml \
#     OTEL=1 OTEL_EXPORTER_OTLP_ENDPOINT=... scripts/eval_swebench.sh swebench_lite.json grok 20
#   MAX_COST    stop launching tasks once cumulative $ crosses this ceiling
#   PRICE_FILE  TOML [pricing] file (model -> USD/Mtok) for per-model cost
#   OTEL        if non-empty, export each task's run to OTLP (needs OTEL_EXPORTER_OTLP_ENDPOINT)
#
# Requires: go, git, python3, and — for the Gate to mean anything — a prepared
# environment in which the target repos' tests can run (see ADR 009 and the
# caveat in swebench_to_tasks.py). With BACKEND=claudecode you also need the
# `claude` CLI authenticated; with grok, a Grok API key in the environment.
set -euo pipefail

INSTANCES="${1:?usage: eval_swebench.sh INSTANCES.json [BACKEND] [LIMIT]}"
BACKEND="${2:-claudecode}"
LIMIT="${3:-0}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "Building aoa..."
go build -o aoa ./cmd/aoa

WORK="$(mktemp -d -t aoa-swebench.XXXXXX)"
TASKS="$WORK/tasks.toml"
echo "Workspace: $WORK"

LIMIT_ARG=()
[ "$LIMIT" != "0" ] && LIMIT_ARG=(--limit "$LIMIT")

echo "Preparing repos + tasks.toml from $INSTANCES ..."
uv run python scripts/swebench_to_tasks.py "$INSTANCES" "$WORK/repos" "$TASKS" "${LIMIT_ARG[@]}"

# Build the eval invocation, folding in the optional cost/OTel knobs.
EVAL_ARGS=(--tasks "$TASKS" --backend "$BACKEND" --json)
[ -n "${PRICE_FILE:-}" ] && EVAL_ARGS+=(--price-file "$PRICE_FILE")
[ -n "${MAX_COST:-}" ]   && EVAL_ARGS+=(--max-cost "$MAX_COST")
[ -n "${OTEL:-}" ]       && EVAL_ARGS+=(--otel)

echo ""
echo "=== Running aoa eval (backend=$BACKEND) ==="
./aoa eval "${EVAL_ARGS[@]}" | tee "$WORK/report.json"

echo ""
echo "Report: $WORK/report.json"
echo "Repos kept at $WORK/repos for inspection."
