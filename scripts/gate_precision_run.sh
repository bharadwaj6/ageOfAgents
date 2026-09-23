#!/usr/bin/env bash
# Resumable, one-instance-at-a-time Gate-precision runner.
#
# Measures Gate precision on SWE-bench Lite one docker image at a time.
# Each image is ~3 GB; this script pulls, uses, and removes it before the
# next one, keeping peak disk to roughly one image.
#
# Usage:
#   scripts/gate_precision_run.sh INSTANCES.json BACKEND
#
# Multi-backend usage (two pre-registered arms, same instances):
#   BACKENDS="agy grok" scripts/gate_precision_run.sh INSTANCES.json
#
# Environment variables (all optional):
#   BACKENDS             — space-separated list of backends to run per instance;
#                          when set, the BACKEND positional argument is ignored
#   SAMPLE               — number of instances to evaluate (default 100)
#   SEED                 — sampling seed (default 103)
#   RUN_DIR              — directory for all run output (default gp-runs/<run-id>)
#   EVAL_DIR             — directory aoa eval runs in; it picks up aoa.toml
#                          (backend plugins, conventions_file) from here
#                          (default: repo root)
#   STOP_AFTER_REJECTIONS — stop once this many gate_valid rejections are seen
#                          per backend (default 30)
#   DRY_RUN=1            — print plan and exit without touching docker/network
#
# aoa eval makes its worktrees under TMPDIR, which this script sets to
# RUN_DIR/tmp. A confined harness (see docs/harnesses/agy.md) refuses to run
# outside its root, so that root must contain RUN_DIR.
#
# Output:
#   RUN_DIR/plan.json       — sampled instance ids (written once)
#   RUN_DIR/results.jsonl   — one JSON line per (instance, backend)
#   RUN_DIR/instances/      — per-instance workdirs (tasks, reports, patches)
#
# Resume: re-run the same command; any (instance, backend) pair already in
# results.jsonl is skipped automatically.
#
# Every aoa eval for an instance — each backend, and the Gate-validity mock
# run — is reset to the commit recorded in instances/<id>/base_sha when the
# instance was prepared (scripts/gate_precision_reset.sh). aoa merges a passing
# change into that repository's main, so without the reset the next eval would
# start from the previous merge. A resumed instance that has tasks.toml but no
# base_sha is recorded as error; the base is not guessed.
#
# Summarise (single backend or with --backend flag):
#   uv run python scripts/precision_summary.py RUN_DIR/results.jsonl [--backend NAME]
set -euo pipefail

INSTANCES="${1:?usage: gate_precision_run.sh INSTANCES.json BACKEND}"
# BACKEND positional arg is used only when BACKENDS env var is not set.
_BACKEND_ARG="${2:-}"

SAMPLE="${SAMPLE:-100}"
SEED="${SEED:-103}"
STOP_AFTER_REJECTIONS="${STOP_AFTER_REJECTIONS:-30}"
DRY_RUN="${DRY_RUN:-}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EVAL_DIR="${EVAL_DIR:-$ROOT}"

RUN_ID="gp-$(date +%Y%m%d-%H%M%S)"
RUN_DIR="${RUN_DIR:-$ROOT/gp-runs/$RUN_ID}"

# Resolve backends list: BACKENDS env var overrides positional BACKEND arg.
if [[ -n "${BACKENDS:-}" ]]; then
    # shellcheck disable=SC2206
    BACKEND_LIST=($BACKENDS)
else
    if [[ -z "$_BACKEND_ARG" ]]; then
        echo "error: supply BACKEND positional arg or set BACKENDS env var" >&2
        exit 1
    fi
    BACKEND_LIST=("$_BACKEND_ARG")
fi

# ── dry-run path (no docker, no network) ───────────────────────────────────
if [[ -n "$DRY_RUN" ]]; then
    echo "=== Gate precision dry run ==="
    echo "  INSTANCES:  $INSTANCES"
    echo "  BACKENDS:   ${BACKEND_LIST[*]}"
    echo "  SAMPLE:     $SAMPLE"
    echo "  SEED:       $SEED"
    echo "  STOP_AFTER_REJECTIONS: $STOP_AFTER_REJECTIONS"
    echo ""

    PLAN_TMP="$(mktemp)"
    trap 'rm -f "$PLAN_TMP"' EXIT
    uv run python "$ROOT/scripts/gate_precision_sample.py" \
        "$INSTANCES" --n "$SAMPLE" --seed "$SEED" --out "$PLAN_TMP"

    N_SAMPLED="$(python3 -c 'import json, sys; print(len(json.load(open(sys.argv[1]))))' "$PLAN_TMP")"
    echo "Sampled: $N_SAMPLED instances"
    echo ""

    echo "Repo mix (top repos by instance count):"
    python3 - "$PLAN_TMP" "$INSTANCES" <<'PYEOF'
import json, sys, collections
plan = json.load(open(sys.argv[1]))
raw = open(sys.argv[2]).read().strip()
all_rows = json.loads(raw) if raw.startswith('[') else \
    [json.loads(l) for l in raw.splitlines() if l.strip()]
idx = {r['instance_id']: r for r in all_rows}
counter = collections.Counter(idx[i]['repo'] for i in plan if i in idx)
for repo, cnt in counter.most_common(5):
    print(f"  {repo}: {cnt}")
PYEOF

    echo ""
    echo "First few image names:"
    python3 - "$PLAN_TMP" <<'PYEOF'
import json, sys
plan = json.load(open(sys.argv[1]))
for iid in plan[:5]:
    escaped = iid.replace('__', '_1776_')
    print(f"  swebench/sweb.eval.x86_64.{escaped}:latest")
if len(plan) > 5:
    print(f"  ... ({len(plan) - 5} more)")
PYEOF

    echo ""
    echo "Assumptions:"
    echo "  ~3 GB peak disk (one image at a time: pull → eval → docker rmi)"
    echo "  ~10–20 minutes per instance per backend"
    echo "  Total estimate: $SAMPLE instances × ${#BACKEND_LIST[@]} backend(s) × 10–20 min"
    exit 0
fi

# ── dependency checks (real run only) ──────────────────────────────────────
for _cmd in docker uv go jq; do
    if ! command -v "$_cmd" &>/dev/null; then
        echo "error: '$_cmd' not found in PATH" >&2
        exit 1
    fi
done

# ── setup ───────────────────────────────────────────────────────────────────
mkdir -p "$RUN_DIR/instances" "$RUN_DIR/tmp"
RESULTS="$RUN_DIR/results.jsonl"
PLAN="$RUN_DIR/plan.json"

echo "=== Gate precision run: $RUN_ID ==="
echo "  RUN_DIR:  $RUN_DIR"
echo "  BACKENDS: ${BACKEND_LIST[*]}"
echo "  SAMPLE:   $SAMPLE  SEED: $SEED  STOP_AFTER: $STOP_AFTER_REJECTIONS"
echo ""

# Build aoa binary once
echo "Building aoa..."
(cd "$ROOT" && go build -o aoa ./cmd/aoa)

# ── sample plan (idempotent) ────────────────────────────────────────────────
if [[ ! -f "$PLAN" ]]; then
    uv run python "$ROOT/scripts/gate_precision_sample.py" \
        "$INSTANCES" --n "$SAMPLE" --seed "$SEED" --out "$PLAN"
fi

# ── helpers ─────────────────────────────────────────────────────────────────

# already_done_pair INSTANCE_ID BACKEND → exit 0 if (iid, backend) result recorded
already_done_pair() {
    local iid="$1"
    local backend="$2"
    [[ -f "$RESULTS" ]] && \
        python3 - "$RESULTS" "$iid" "$backend" <<'PYEOF'
import json, sys
rows = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
iid, be = sys.argv[2], sys.argv[3]
sys.exit(0 if any(r.get('instance_id') == iid and r.get('backend') == be for r in rows) else 1)
PYEOF
}

# docker image name matching the adapter's escaping
image_name() {
    local iid="$1"
    local escaped="${iid//__/_1776_}"
    echo "swebench/sweb.eval.x86_64.${escaped}:latest"
}

# count_gate_valid_rejections_for BACKEND → prints integer
count_gate_valid_rejections_for() {
    python3 "$ROOT/scripts/aoa_eval_report.py" rejections "$RESULTS" "$1"
}

# append_result iid repo backend outcome gate_valid oracle tokens seconds
append_result() {
    python3 -c "
import json, sys
print(json.dumps({
    'instance_id': sys.argv[1],
    'repo': sys.argv[2],
    'backend': sys.argv[3],
    'outcome': sys.argv[4],
    'gate_valid': None if sys.argv[5] == 'null' else (sys.argv[5] == 'true'),
    'oracle': None if sys.argv[6] == 'null' else sys.argv[6],
    'tokens': int(sys.argv[7]),
    'seconds': float(sys.argv[8]),
}))
" "$1" "$2" "$3" "$4" "$5" "$6" "$7" "$8" >> "$RESULTS"
}

# instance_repo INSTANCE_ID → repo string
instance_repo() {
    python3 - "$INSTANCES" "$1" <<'PYEOF'
import json, sys
raw = open(sys.argv[1]).read().strip()
rows = json.loads(raw) if raw.startswith('[') else \
    [json.loads(l) for l in raw.splitlines() if l.strip()]
idx = {r['instance_id']: r for r in rows}
print(idx.get(sys.argv[2], {}).get('repo', ''))
PYEOF
}

# report FIELD AOA_REPORT IID → outcome (merged|rejected|infra|error) or tokens
report() {
    python3 "$ROOT/scripts/aoa_eval_report.py" "$@"
}

# is_backend_stopped BACKEND → exit 0 if that backend has reached its stop limit
is_backend_stopped() {
    local backend="$1"
    local cnt
    cnt="$(count_gate_valid_rejections_for "$backend")"
    (( cnt >= STOP_AFTER_REJECTIONS ))
}

# ── per-instance loop ───────────────────────────────────────────────────────
while IFS= read -r IID; do

    # Check if every backend has already stopped (reached its own limit).
    ALL_STOPPED=true
    for BACKEND in "${BACKEND_LIST[@]}"; do
        if ! is_backend_stopped "$BACKEND"; then
            ALL_STOPPED=false
            break
        fi
    done
    if [[ "$ALL_STOPPED" == "true" ]]; then
        echo "All backends reached STOP_AFTER_REJECTIONS=$STOP_AFTER_REJECTIONS. Stopping."
        break
    fi

    REPO="$(instance_repo "$IID")"
    IMAGE="$(image_name "$IID")"
    INST_DIR="$RUN_DIR/instances/$IID"
    mkdir -p "$INST_DIR"

    # Determine which backends still need to run for this instance.
    ACTIVE_BACKENDS=()
    for BACKEND in "${BACKEND_LIST[@]}"; do
        if is_backend_stopped "$BACKEND"; then
            continue
        fi
        if already_done_pair "$IID" "$BACKEND"; then
            echo "  skip $IID/$BACKEND (already in results.jsonl)"
            continue
        fi
        ACTIVE_BACKENDS+=("$BACKEND")
    done

    if [[ ${#ACTIVE_BACKENDS[@]} -eq 0 ]]; then
        continue
    fi

    echo ""
    echo "=== $IID (backends: ${ACTIVE_BACKENDS[*]}) ==="

    # Prepare instance files once (idempotent).
    ONE_INST="$INST_DIR/one_instance.json"
    if [[ ! -f "$ONE_INST" ]]; then
        python3 - "$INSTANCES" "$IID" "$ONE_INST" <<'PYEOF'
import json, sys
raw = open(sys.argv[1]).read().strip()
rows = json.loads(raw) if raw.startswith('[') else \
    [json.loads(l) for l in raw.splitlines() if l.strip()]
json.dump([r for r in rows if r['instance_id'] == sys.argv[2]], open(sys.argv[3], 'w'))
PYEOF
    fi

    TASKS="$INST_DIR/tasks.toml"
    REPOS_DIR="$INST_DIR/repos"
    REPO_DIR="$REPOS_DIR/$IID"
    BASE_SHA_FILE="$INST_DIR/base_sha"
    mkdir -p "$REPOS_DIR"
    if [[ ! -f "$TASKS" ]]; then
        uv run python "$ROOT/scripts/swebench_to_tasks.py" \
            "$ONE_INST" "$REPOS_DIR" "$TASKS" \
            --gate repo --max-attempts 1
        # Once, at prepare time. A later resume must not re-read HEAD: main may
        # already contain a merge from an eval that ran before the runner stopped.
        git -C "$REPO_DIR" rev-parse HEAD > "$BASE_SHA_FILE"
    elif [[ ! -f "$BASE_SHA_FILE" ]]; then
        echo "  FAILED: $IID has tasks.toml but no base_sha — recording error for all active backends"
        for BACKEND in "${ACTIVE_BACKENDS[@]}"; do
            append_result "$IID" "$REPO" "$BACKEND" "error" "null" "null" 0 0
        done
        continue
    fi

    # Pull image once (only if at least one backend still needs it).
    rm -f "$INST_DIR/image_pulled"   # a marker left by an earlier attempt must not vouch for this pull
    (
        set -euo pipefail
        echo "  docker pull $IMAGE"
        docker pull --platform linux/amd64 "$IMAGE"
        echo "ok" > "$INST_DIR/image_pulled"
    ) 2>&1 | tee -a "$INST_DIR/run.log" || true

    if [[ ! -f "$INST_DIR/image_pulled" ]]; then
        echo "  FAILED: could not pull image for $IID — recording error for all active backends"
        for BACKEND in "${ACTIVE_BACKENDS[@]}"; do
            append_result "$IID" "$REPO" "$BACKEND" "error" "null" "null" 0 0
        done
        continue
    fi

    # Gate-validity result cache for this instance (run at most once).
    # "unknown" = not yet checked; "true"/"false" = result cached.
    MOCK_RESULT_FILE="$INST_DIR/mock_result"

    # ── per-backend loop ────────────────────────────────────────────────
    for BACKEND in "${ACTIVE_BACKENDS[@]}"; do
        T_START="$(date +%s)"

        BACKEND_STATUS="$INST_DIR/status.$BACKEND"
        rm -f "$BACKEND_STATUS"

        (
            set -euo pipefail

            AOA_REPORT="$INST_DIR/aoa_report.$BACKEND.json"

            # The previous arm may have merged into main. Start from the base
            # recorded when this instance was prepared.
            "$ROOT/scripts/gate_precision_reset.sh" \
                "$REPO_DIR" "$(cat "$BASE_SHA_FILE")"

            # Run aoa eval for this backend.
            (cd "$EVAL_DIR" && \
                TMPDIR="$RUN_DIR/tmp" "$ROOT/aoa" eval \
                    --tasks "$TASKS" \
                    --backend "$BACKEND" \
                    --json \
            ) > "$AOA_REPORT"

            CLS="$(report outcome "$AOA_REPORT" "$IID")"
            TOK="$(report tokens  "$AOA_REPORT" "$IID")"
            GV="null"
            ORA="null"

            if [[ "$CLS" == "rejected" ]]; then
                # Gate validity: run mock at most once per instance.
                if [[ ! -f "$MOCK_RESULT_FILE" ]]; then
                    MOCK_REPORT="$INST_DIR/mock_report.json"
                    # The arm above may have merged. The validity run has to
                    # see the same pristine base as every arm.
                    "$ROOT/scripts/gate_precision_reset.sh" \
                        "$REPO_DIR" "$(cat "$BASE_SHA_FILE")"
                    (cd "$EVAL_DIR" && \
                        TMPDIR="$RUN_DIR/tmp" "$ROOT/aoa" eval \
                            --tasks "$TASKS" \
                            --backend mock \
                            --json \
                    ) > "$MOCK_REPORT"
                    if [[ "$(report outcome "$MOCK_REPORT" "$IID")" == merged ]]; then
                        echo "true" > "$MOCK_RESULT_FILE"
                    else
                        echo "false" > "$MOCK_RESULT_FILE"
                    fi
                fi
                GV="$(cat "$MOCK_RESULT_FILE")"

                # Oracle: score the rejected patch for this backend.
                PREDICTIONS="$INST_DIR/rejected_predictions.$BACKEND.json"
                uv run python "$ROOT/scripts/gate_precision.py" \
                    "$AOA_REPORT" "$PREDICTIONS"

                # Run swebench harness with a backend-scoped run id.
                HARNESS_RUN_ID="gp-${IID//\//_}-${BACKEND}-$(date +%s)"
                (cd "$EVAL_DIR" && \
                    uv run --with "swebench==4.1.0" \
                        python -m swebench.harness.run_evaluation \
                        --predictions_path "$PREDICTIONS" \
                        --run_id "$HARNESS_RUN_ID" \
                        --max_workers 1 \
                        --cache_level env \
                        --dataset_name "princeton-nlp/SWE-bench_Lite" \
                        --split test \
                )

                MODEL_ESCAPED="aoa-rejected"
                REPORT_PATH="$EVAL_DIR/logs/run_evaluation/$HARNESS_RUN_ID/$MODEL_ESCAPED/$IID/report.json"
                if [[ ! -f "$REPORT_PATH" ]]; then
                    REPORT_PATH="$(find "$EVAL_DIR/logs/run_evaluation/$HARNESS_RUN_ID" \
                        -name "report.json" -path "*/$IID/report.json" 2>/dev/null | head -1 || true)"
                fi

                if [[ -n "$REPORT_PATH" && -f "$REPORT_PATH" ]]; then
                    ORA="$(python3 - "$REPORT_PATH" "$IID" 2>/dev/null <<'PYEOF' || echo error
import json, sys
inst = json.load(open(sys.argv[1])).get(sys.argv[2], {})
print('resolved' if inst.get('resolved') is True else 'unresolved' if 'resolved' in inst else 'error')
PYEOF
)"
                else
                    ORA="error"
                fi
            fi

            echo "$CLS $GV $ORA $TOK" > "$BACKEND_STATUS"
        ) 2>&1 | tee -a "$INST_DIR/run.$BACKEND.log" || true

        SECS=$(( $(date +%s) - T_START ))

        if [[ -f "$BACKEND_STATUS" ]]; then
            read -r B_OUTCOME B_GV B_ORA B_TOK < "$BACKEND_STATUS"
        else
            echo "  FAILED: no status written for $IID/$BACKEND — recording error"
            B_OUTCOME="error"
            B_GV="null"
            B_ORA="null"
            B_TOK=0
        fi

        append_result "$IID" "$REPO" "$BACKEND" "$B_OUTCOME" "$B_GV" "$B_ORA" "$B_TOK" "$SECS"
        echo "  → backend=$BACKEND outcome=$B_OUTCOME gate_valid=$B_GV oracle=$B_ORA tokens=$B_TOK seconds=${SECS}s"
    done

    # Remove image once all backends are done with this instance.
    docker rmi "$IMAGE" 2>/dev/null || true

done < <(python3 -c 'import json, sys; print(*json.load(open(sys.argv[1])), sep="\n")' "$PLAN")

echo ""
echo "=== Done ==="
echo "  results:   $RESULTS"
echo ""
echo "Gate-valid rejections per backend:"
for BACKEND in "${BACKEND_LIST[@]}"; do
    CNT="$(count_gate_valid_rejections_for "$BACKEND")"
    echo "  $BACKEND: $CNT / $STOP_AFTER_REJECTIONS"
done
echo ""
echo "Summarise:"
for BACKEND in "${BACKEND_LIST[@]}"; do
    echo "  uv run python scripts/precision_summary.py \"$RESULTS\" --backend \"$BACKEND\""
done
