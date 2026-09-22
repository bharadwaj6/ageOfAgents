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
# Environment variables (all optional):
#   SAMPLE               — number of instances to evaluate (default 100)
#   SEED                 — sampling seed (default 103)
#   RUN_DIR              — directory for all run output (default gp-runs/<run-id>)
#   EVAL_DIR             — directory aoa eval runs in; it picks up aoa.toml
#                          (backend plugins, conventions_file) from here
#                          (default: repo root)
#   STOP_AFTER_REJECTIONS — stop once this many gate_valid rejections are seen
#                          (default 30)
#   DRY_RUN=1            — print plan and exit without touching docker/network
#
# Output:
#   RUN_DIR/plan.json       — sampled instance ids (written once)
#   RUN_DIR/results.jsonl   — one JSON line per completed instance
#   RUN_DIR/instances/      — per-instance workdirs (tasks, reports, patches)
#
# Resume: re-run the same command; any instance id already in results.jsonl
# is skipped automatically.
#
# Summarise:
#   uv run python scripts/gate_precision.py RUN_DIR/results.jsonl ...
set -euo pipefail

INSTANCES="${1:?usage: gate_precision_run.sh INSTANCES.json BACKEND}"
BACKEND="${2:?usage: gate_precision_run.sh INSTANCES.json BACKEND}"

SAMPLE="${SAMPLE:-100}"
SEED="${SEED:-103}"
STOP_AFTER_REJECTIONS="${STOP_AFTER_REJECTIONS:-30}"
DRY_RUN="${DRY_RUN:-}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
EVAL_DIR="${EVAL_DIR:-$ROOT}"

RUN_ID="gp-$(date +%Y%m%d-%H%M%S)"
RUN_DIR="${RUN_DIR:-$ROOT/gp-runs/$RUN_ID}"

# ── dry-run path (no docker, no network) ───────────────────────────────────
if [[ -n "$DRY_RUN" ]]; then
    echo "=== Gate precision dry run ==="
    echo "  INSTANCES:  $INSTANCES"
    echo "  BACKEND:    $BACKEND"
    echo "  SAMPLE:     $SAMPLE"
    echo "  SEED:       $SEED"
    echo "  STOP_AFTER_REJECTIONS: $STOP_AFTER_REJECTIONS"
    echo ""

    PLAN_TMP="$(mktemp)"
    trap 'rm -f "$PLAN_TMP"' EXIT
    uv run python "$ROOT/scripts/gate_precision_sample.py" \
        "$INSTANCES" --n "$SAMPLE" --seed "$SEED" --out "$PLAN_TMP"

    N_SAMPLED="$(python3 -c "import json; print(len(json.load(open('$PLAN_TMP'))))")"
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
    echo "  ~10–20 minutes per instance"
    echo "  Total estimate: $SAMPLE instances × 10–20 min = $((SAMPLE * 10))–$((SAMPLE * 20)) minutes"
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
mkdir -p "$RUN_DIR/instances"
RESULTS="$RUN_DIR/results.jsonl"
PLAN="$RUN_DIR/plan.json"

echo "=== Gate precision run: $RUN_ID ==="
echo "  RUN_DIR:  $RUN_DIR"
echo "  BACKEND:  $BACKEND"
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

# already_done INSTANCE_ID → exit 0 if result recorded, else exit 1
already_done() {
    local iid="$1"
    [[ -f "$RESULTS" ]] && grep -q "\"instance_id\": \"$iid\"" "$RESULTS"
}

# docker image name matching the adapter's escaping
image_name() {
    local iid="$1"
    local escaped="${iid//__/_1776_}"
    echo "swebench/sweb.eval.x86_64.${escaped}:latest"
}

# count_gate_valid_rejections → prints integer
count_gate_valid_rejections() {
    if [[ ! -f "$RESULTS" ]]; then echo 0; return; fi
    python3 -c "
import json
count = 0
for line in open('$RESULTS'):
    r = json.loads(line)
    if r.get('outcome') == 'rejected' and r.get('gate_valid') is True:
        count += 1
print(count)
"
}

# append_result iid repo outcome gate_valid oracle tokens seconds
append_result() {
    python3 -c "
import json, sys
print(json.dumps({
    'instance_id': sys.argv[1],
    'repo': sys.argv[2],
    'outcome': sys.argv[3],
    'gate_valid': None if sys.argv[4] == 'null' else (sys.argv[4] == 'true'),
    'oracle': None if sys.argv[5] == 'null' else sys.argv[5],
    'tokens': int(sys.argv[6]),
    'seconds': float(sys.argv[7]),
}))
" "$1" "$2" "$3" "$4" "$5" "$6" "$7" >> "$RESULTS"
}

# instance_repo INSTANCE_ID → repo string
instance_repo() {
    local iid="$1"
    python3 -c "
import json
raw = open('$INSTANCES').read().strip()
rows = json.loads(raw) if raw.startswith('[') else \
    [json.loads(l) for l in raw.splitlines() if l.strip()]
idx = {r['instance_id']: r for r in rows}
print(idx.get('$iid', {}).get('repo', ''))
"
}

# read_tokens AOA_REPORT IID → token count (0 if not found)
read_tokens() {
    python3 -c "
import json
reports = json.load(open('$1'))
rep = next((r for r in reports if r.get('task') == '$2'), None)
print(rep.get('tokens', 0) if rep else 0)
"
}

# classify_outcome AOA_REPORT IID → merged|rejected|infra|error
classify_outcome() {
    python3 -c "
import json, sys
reports = json.load(open('$1'))
rep = next((r for r in reports if r.get('task') == '$2'), None)
if rep is None:
    print('error'); sys.exit(0)
rejected = rep.get('rejected_patches') or []
real_rejects = [r for r in rejected if 'sandbox failure' not in (r.get('reason') or '')]
sandbox_fails = [r for r in rejected if 'sandbox failure' in (r.get('reason') or '')]
if rep.get('merged'):
    print('merged')
elif real_rejects:
    print('rejected')
elif sandbox_fails:
    print('infra')
else:
    print('error')
"
}

# ── per-instance loop ───────────────────────────────────────────────────────
while IFS= read -r IID; do
    GATE_VALID_COUNT="$(count_gate_valid_rejections)"
    if (( GATE_VALID_COUNT >= STOP_AFTER_REJECTIONS )); then
        echo "Reached STOP_AFTER_REJECTIONS=$STOP_AFTER_REJECTIONS gate-valid rejections. Stopping."
        break
    fi

    if already_done "$IID"; then
        echo "  skip $IID (already in results.jsonl)"
        continue
    fi

    echo ""
    echo "=== $IID ==="
    T_START="$(date +%s%N)"

    REPO="$(instance_repo "$IID")"
    IMAGE="$(image_name "$IID")"
    INST_DIR="$RUN_DIR/instances/$IID"
    mkdir -p "$INST_DIR"

    OUTCOME="error"
    GATE_VALID_VAL="null"
    ORACLE_VAL="null"
    TOKENS=0

    # Use a temp file to communicate per-instance success to the outer shell.
    INST_STATUS="$INST_DIR/status"
    rm -f "$INST_STATUS"

    # Subshell per instance: failures record 'error' without aborting the run.
    (
        set -euo pipefail

        # 1. Pull image
        echo "  docker pull $IMAGE"
        docker pull --platform linux/amd64 "$IMAGE"

        # 2. Prepare a one-instance JSON and build tasks.toml
        ONE_INST="$INST_DIR/one_instance.json"
        python3 -c "
import json
raw = open('$INSTANCES').read().strip()
rows = json.loads(raw) if raw.startswith('[') else \
    [json.loads(l) for l in raw.splitlines() if l.strip()]
match = [r for r in rows if r['instance_id'] == '$IID']
json.dump(match, open('$ONE_INST', 'w'))
"

        TASKS="$INST_DIR/tasks.toml"
        REPOS_DIR="$INST_DIR/repos"
        mkdir -p "$REPOS_DIR"
        uv run python "$ROOT/scripts/swebench_to_tasks.py" \
            "$ONE_INST" "$REPOS_DIR" "$TASKS" \
            --gate repo --max-attempts 1

        # Run aoa eval from EVAL_DIR so it picks up its aoa.toml
        AOA_REPORT="$INST_DIR/aoa_report.json"
        (cd "$EVAL_DIR" && \
            "$ROOT/aoa" eval \
                --tasks "$TASKS" \
                --backend "$BACKEND" \
                --json \
        ) > "$AOA_REPORT"

        # 3. Classify outcome
        CLS="$(classify_outcome "$AOA_REPORT" "$IID")"
        TOK="$(read_tokens "$AOA_REPORT" "$IID")"
        GV="null"
        ORA="null"

        # 4. If rejected: gate validity + oracle
        if [[ "$CLS" == "rejected" ]]; then
            # Gate validity: re-run with mock backend (null patch)
            MOCK_REPORT="$INST_DIR/mock_report.json"
            (cd "$EVAL_DIR" && \
                "$ROOT/aoa" eval \
                    --tasks "$TASKS" \
                    --backend mock \
                    --json \
            ) > "$MOCK_REPORT"

            GV="$(python3 -c "
import json
reports = json.load(open('$MOCK_REPORT'))
rep = next((r for r in reports if r.get('task') == '$IID'), None)
print('true' if rep and rep.get('merged') else 'false')
")"

            # Oracle: turn rejected patch into a SWE-bench prediction and score
            PREDICTIONS="$INST_DIR/rejected_predictions.json"
            uv run python "$ROOT/scripts/gate_precision.py" \
                "$AOA_REPORT" "$PREDICTIONS"

            # Run swebench harness (pinned to 4.1.0, matches eval_swebench_docker.sh)
            # swebench 4.1.0: RUN_EVALUATION_LOG_DIR = "logs/run_evaluation"
            # per-instance report: logs/run_evaluation/<run_id>/<model>/<instance_id>/report.json
            HARNESS_RUN_ID="gp-${IID//\//_}-$(date +%s)"
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

            # model_name_or_path from gate_precision.py default is "aoa-rejected"
            MODEL_ESCAPED="aoa-rejected"
            REPORT_PATH="$EVAL_DIR/logs/run_evaluation/$HARNESS_RUN_ID/$MODEL_ESCAPED/$IID/report.json"
            if [[ ! -f "$REPORT_PATH" ]]; then
                # Fallback: search if model name contained slashes (replaced with __)
                REPORT_PATH="$(find "$EVAL_DIR/logs/run_evaluation/$HARNESS_RUN_ID" \
                    -name "report.json" -path "*/$IID/report.json" 2>/dev/null | head -1 || true)"
            fi

            if [[ -n "$REPORT_PATH" && -f "$REPORT_PATH" ]]; then
                ORA="$(python3 -c "
import json, sys
data = json.load(open('$REPORT_PATH'))
inst = data.get('$IID', {})
if inst.get('resolved') is True:
    print('resolved')
elif 'resolved' in inst:
    print('unresolved')
else:
    print('error')
" 2>/dev/null || echo "error")"
            else
                ORA="error"
            fi
        fi

        # 5. Remove image (best-effort)
        docker rmi "$IMAGE" 2>/dev/null || true

        # Signal success with outcome details
        echo "$CLS $GV $ORA $TOK" > "$INST_STATUS"
    ) 2>&1 | tee "$INST_DIR/run.log" || true

    T_END="$(date +%s%N)"
    SECS="$(python3 -c "print(round(($T_END - $T_START) / 1e9, 2))")"

    # Read outcome from status file (set inside subshell)
    if [[ -f "$INST_STATUS" ]]; then
        read -r OUTCOME GATE_VALID_VAL ORACLE_VAL TOKENS < "$INST_STATUS"
    else
        echo "  FAILED: no status written for $IID — recording error"
        OUTCOME="error"
        GATE_VALID_VAL="null"
        ORACLE_VAL="null"
        TOKENS=0
        docker rmi "$IMAGE" 2>/dev/null || true
    fi

    append_result "$IID" "$REPO" "$OUTCOME" "$GATE_VALID_VAL" "$ORACLE_VAL" "$TOKENS" "$SECS"
    echo "  → outcome=$OUTCOME gate_valid=$GATE_VALID_VAL oracle=$ORACLE_VAL tokens=$TOKENS seconds=${SECS}s"
done < <(python3 -c "import json; [print(i) for i in json.load(open('$PLAN'))]")

echo ""
echo "=== Done ==="
echo "  results:   $RESULTS"
echo "  Summarise: uv run python scripts/gate_precision.py --help"
echo ""
FINAL_COUNT="$(count_gate_valid_rejections)"
echo "Gate-valid rejections: $FINAL_COUNT / $STOP_AFTER_REJECTIONS"
