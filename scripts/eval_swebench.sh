#!/usr/bin/env bash
# Run aoa end-to-end on SWE-bench (Lite) tasks with the real claudecode backend.
#
#   scripts/eval_swebench.sh INSTANCES.json [BACKEND] [LIMIT]
#
#   INSTANCES.json  SWE-bench instances (JSON array or JSONL). Pull the Lite
#                   split from HuggingFace (princeton-nlp/SWE-bench_Lite) and
#                   export it to JSON; this script never touches the network for
#                   the dataset itself.
#   BACKEND         mock | claudecode   (default: claudecode)
#   LIMIT           only the first N instances (default: all)
#
# Requires: go, git, python3, and — for the Gate to mean anything — a prepared
# environment in which the target repos' tests can run (see ADR 009 and the
# caveat in swebench_to_tasks.py). With BACKEND=claudecode you also need the
# `claude` CLI authenticated.
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
python3 scripts/swebench_to_tasks.py "$INSTANCES" "$WORK/repos" "$TASKS" "${LIMIT_ARG[@]}"

echo ""
echo "=== Running aoa eval (backend=$BACKEND) ==="
./aoa eval --tasks "$TASKS" --backend "$BACKEND" --json | tee "$WORK/report.json"

echo ""
echo "Report: $WORK/report.json"
echo "Repos kept at $WORK/repos for inspection."
