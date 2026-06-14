#!/usr/bin/env bash
# Proper SWE-bench evaluation via the official Docker harness.
#
# Three phases:
#   1. aoa eval --inference-mode  — agent generates patches (no-op Gate so
#      merges always proceed; the Docker harness is the real verifier).
#   2. extract_swebench_patches.py — collect git diffs → predictions.json.
#   3. swebench.harness.run_evaluation — build Docker images, apply patches,
#      run FAIL_TO_PASS / PASS_TO_PASS tests, report resolved count.
#
# Usage:
#   scripts/eval_swebench_docker.sh INSTANCES.json [BACKEND] [LIMIT] [RUN_ID]
#
#   INSTANCES.json  SWE-bench instances (JSON array or JSONL).
#                   Use scripts/swebench_lite.json (already downloaded).
#   BACKEND         mock | claudecode   (default: claudecode)
#   LIMIT           only the first N instances (default: all)
#   RUN_ID          label for this run (default: aoa-YYYYMMDD-HHMMSS)
#
# Requires: go, git, uv, docker (running), and — with BACKEND=claudecode —
# an authenticated `claude` CLI.
set -euo pipefail

INSTANCES="${1:?usage: eval_swebench_docker.sh INSTANCES.json [BACKEND] [LIMIT] [RUN_ID]}"
BACKEND="${2:-claudecode}"
LIMIT="${3:-0}"
RUN_ID="${4:-aoa-$(date +%Y%m%d-%H%M%S)}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

# ── sanity checks ──────────────────────────────────────────────────────────────
if ! docker info &>/dev/null; then
    echo "error: Docker daemon is not running. Start Docker Desktop and retry." >&2
    exit 1
fi

# ── build ──────────────────────────────────────────────────────────────────────
echo "Building aoa..."
go build -o aoa ./cmd/aoa

WORK="$(mktemp -d -t aoa-swebench.XXXXXX)"
TASKS="$WORK/tasks.toml"
PREDICTIONS="$WORK/predictions.json"
AOA_REPORT="$WORK/aoa_report.json"
echo "Workspace: $WORK"
echo "Run ID:    $RUN_ID"
echo ""

# ── Phase 1: inference ─────────────────────────────────────────────────────────
LIMIT_ARG=()
[ "$LIMIT" != "0" ] && LIMIT_ARG=(--limit "$LIMIT")

echo "=== Phase 1: preparing repos + tasks.toml (inference mode) ==="
uv run python scripts/swebench_to_tasks.py \
    "$INSTANCES" "$WORK/repos" "$TASKS" "${LIMIT_ARG[@]}" --inference-mode

echo ""
echo "=== Running aoa eval (backend=$BACKEND, inference mode) ==="
./aoa eval --tasks "$TASKS" --backend "$BACKEND" --json | tee "$AOA_REPORT"

# ── Phase 2: extract patches ───────────────────────────────────────────────────
echo ""
echo "=== Phase 2: extracting patches → $PREDICTIONS ==="
uv run python scripts/extract_swebench_patches.py \
    "$TASKS" "$PREDICTIONS" --model "aoa-$BACKEND"

PATCH_COUNT=$(python3 -c "import json; d=json.load(open('$PREDICTIONS')); print(sum(1 for p in d if p['model_patch']))")
echo "Non-empty patches: $PATCH_COUNT"

# ── Phase 3: official Docker evaluation ────────────────────────────────────────
echo ""
echo "=== Phase 3: official SWE-bench Docker evaluation (run_id=$RUN_ID) ==="
echo "    This builds per-instance Docker images and runs FAIL_TO_PASS tests."
echo "    First run will be slow (~minutes per image); subsequent runs use the cache."
echo ""
uv run --with "swebench[eval]" python -m swebench.harness.run_evaluation \
    --predictions_path "$PREDICTIONS" \
    --run_id "$RUN_ID" \
    --max_workers 2 \
    --cache_level env \
    --dataset_name "princeton-nlp/SWE-bench_Lite" \
    --split test

# ── summary ────────────────────────────────────────────────────────────────────
echo ""
echo "=== Done ==="
echo "  aoa report:         $AOA_REPORT"
echo "  predictions:        $PREDICTIONS"
echo "  SWE-bench logs:     logs/run_evaluation/$RUN_ID/"
echo "  Repos (kept):       $WORK/repos/"
echo ""
echo "Resolved count (from harness logs):"
grep -rh '"resolved":' "logs/run_evaluation/$RUN_ID/" 2>/dev/null \
    | awk -F: '{gsub(/[ ,]/, "", $2); print $2}' \
    | sort | uniq -c | sort -rn \
    || echo "  (no log entries found yet — check logs/run_evaluation/$RUN_ID/)"
