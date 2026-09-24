#!/usr/bin/env bash
# Score proposals the Gate merged, with the official SWE-bench harness.
#
# Precision says how often a rejection was right. This asks the complementary
# question: of the proposals the Gate let through, how many break tests that
# already passed? A Gate miss is a merged arm with at least one PASS_TO_PASS
# failure under swebench==4.1.0.
#
# Usage:
#   scripts/gate_miss_score.sh RUN_DIR [BACKENDS]
#
# BACKENDS is the arm order, space-separated ("agy grok" or two arguments).
# It is required only for a run that predates merged_sha.<backend>: merge
# entries from `git reflog show main`, oldest first, map onto that order.
# There must be exactly one merge entry per merged arm; otherwise the
# instance is recorded as error and nothing is guessed.
#
# For each instance in RUN_DIR/instances with a merged arm in results.jsonl:
#   recover the patch (git diff base_sha merged_sha in the task repository),
#   write one prediction (model_name_or_path aoa-merged-<backend>),
#   pull the instance image once, score every pending arm, then docker rmi.
#
# Output: RUN_DIR/merged_scores.jsonl — one JSON line per (instance, backend):
#   instance_id, repo, backend, resolved (bool or null), f2p_failed,
#   p2p_failed (test ids from report.json tests_status), error (null or a
#   short reason). Pairs already in the file are skipped, so a stopped run
#   resumes.
#
# Summarise:
#   uv run python scripts/gate_miss_summary.py RUN_DIR/merged_scores.jsonl [--backend NAME]
set -euo pipefail

RUN_DIR="${1:?usage: gate_miss_score.sh RUN_DIR [BACKENDS]}"
shift

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
mkdir -p "$RUN_DIR"
RUN_DIR="$(cd "$RUN_DIR" && pwd)"

BACKEND_LIST=()
for _arg in "$@"; do
    # shellcheck disable=SC2086
    for _word in $_arg; do
        BACKEND_LIST+=("$_word")
    done
done

for _cmd in docker uv git; do
    if ! command -v "$_cmd" >/dev/null 2>&1; then
        echo "error: '$_cmd' not found in PATH" >&2
        exit 1
    fi
done

RESULTS="$RUN_DIR/results.jsonl"
SCORES="$RUN_DIR/merged_scores.jsonl"
if [[ ! -f "$RESULTS" ]]; then
    echo "error: no results.jsonl in $RUN_DIR" >&2
    exit 1
fi

# A run written after the runner learned to record merges has at least one
# merged_sha file. Older runs have none, and the reflog is the only record.
shopt -s nullglob
_sha_files=("$RUN_DIR"/instances/*/merged_sha.*)
if [[ ${#_sha_files[@]} -eq 0 ]]; then
    USE_REFLOG=1
else
    USE_REFLOG=0
fi
shopt -u nullglob
unset _sha_files

# already_scored IID BACKEND → 0 recorded, 1 not, 2 corrupt file
already_scored() {
    python3 - "$SCORES" "$1" "$2" <<'PY'
import json, sys
from pathlib import Path
path, iid, backend = sys.argv[1:]
file = Path(path)
if not file.exists():
    sys.exit(1)
for raw in file.read_text(encoding="utf-8").splitlines():
    raw = raw.strip()
    if not raw:
        continue
    try:
        row = json.loads(raw)
    except json.JSONDecodeError:
        print(f"error: invalid JSON in {path}", file=sys.stderr)
        sys.exit(2)
    if row.get("instance_id") == iid and row.get("backend") == backend:
        sys.exit(0)
sys.exit(1)
PY
}

# append_score IID REPO BACKEND RESOLVED F2P_JSON P2P_JSON ERROR
# RESOLVED is true, false, or null. ERROR is null or a short reason.
append_score() {
    python3 - "$SCORES" "$1" "$2" "$3" "$4" "$5" "$6" "$7" <<'PY'
import json, sys
path, iid, repo, backend, resolved, f2p, p2p, error = sys.argv[1:]
row = {
    "instance_id": iid,
    "repo": repo,
    "backend": backend,
    "resolved": {"true": True, "false": False, "null": None}[resolved],
    "f2p_failed": json.loads(f2p),
    "p2p_failed": json.loads(p2p),
    "error": None if error == "null" else error,
}
with open(path, "a", encoding="utf-8") as handle:
    handle.write(json.dumps(row) + "\n")
PY
}

append_error() {
    append_score "$1" "$2" "$3" null '[]' '[]' "$4"
}

# Merge commits on main, oldest first. `git reflog show` is newest first;
# a merge entry is a subject whose first word is "merge".
reflog_merge_shas() {
    git -C "$1" --no-pager reflog show main --format='%H %gs' \
        | awk '$2 == "merge" { lines[++n] = $1 } END { for (i = n; i >= 1; i--) print lines[i] }'
}

image_name() {
    local iid="$1"
    local escaped="${iid//__/_1776_}"
    echo "swebench/sweb.eval.x86_64.${escaped}:latest"
}

read_sha() {
    tr -d '[:space:]' < "$1"
}

# Passed to the lister as one comma-separated argument so an empty arm list
# does not expand under set -u.
backend_csv=""
for (( _i = 0; _i < ${#BACKEND_LIST[@]}; _i++ )); do
    if [[ -n "$backend_csv" ]]; then
        backend_csv+=","
    fi
    backend_csv+="${BACKEND_LIST[$_i]}"
done

# One row per instance: id, repo, merged backends in BACKENDS order, and any
# merged backend the caller left out of BACKENDS. A failure here is the run's
# results file, not one arm, so it aborts rather than scoring a partial list.
LISTING="$RUN_DIR/.merged_arms.tsv"
python3 - "$RESULTS" "$backend_csv" > "$LISTING" <<'PY'
import json, sys
from pathlib import Path

results_path, backend_csv = sys.argv[1:]
backend_list = [part for part in backend_csv.split(",") if part]
rows = []
for raw in Path(results_path).read_text(encoding="utf-8").splitlines():
    raw = raw.strip()
    if not raw:
        continue
    row = json.loads(raw)
    if isinstance(row, dict):
        rows.append(row)

groups: dict[str, dict[str, object]] = {}
order: list[str] = []
for row in rows:
    if row.get("outcome") != "merged":
        continue
    iid = str(row.get("instance_id", ""))
    backend = row.get("backend")
    if not iid or not isinstance(backend, str) or not backend:
        continue
    if iid not in groups:
        groups[iid] = {"repo": str(row.get("repo", "")), "backends": []}
        order.append(iid)
    backends = groups[iid]["backends"]
    if isinstance(backends, list) and backend not in backends:
        backends.append(backend)

for iid in order:
    group = groups[iid]
    backends = group["backends"]
    if not isinstance(backends, list):
        continue
    merged = [str(b) for b in backends]
    repo = str(group["repo"])
    if backend_list:
        known = set(backend_list)
        ordered = [b for b in backend_list if b in set(merged)]
        unordered = [b for b in merged if b not in known]
    else:
        ordered = list(merged)
        unordered = []
    print("\t".join([iid, repo, ",".join(ordered), ",".join(unordered)]))
PY

while IFS=$'\t' read -r IID REPO BACKENDS_CSV UNORDERED_CSV; do
    [[ -z "$IID" ]] && continue
    INST_DIR="$RUN_DIR/instances/$IID"
    REPO_DIR="$INST_DIR/repos/$IID"
    mkdir -p "$INST_DIR"

    ordered=()
    if [[ -n "$BACKENDS_CSV" ]]; then
        IFS=',' read -r -a ordered <<< "$BACKENDS_CSV"
    fi
    unordered=()
    if [[ -n "$UNORDERED_CSV" ]]; then
        IFS=',' read -r -a unordered <<< "$UNORDERED_CSV"
    fi

    # Everyone still missing a score line. Index loops: an empty array with
    # set -u is an error on bash 3.2 when expanded as "${arr[@]}".
    pending=()
    everyone=()
    for (( _i = 0; _i < ${#ordered[@]}; _i++ )); do
        everyone+=("${ordered[$_i]}")
    done
    for (( _i = 0; _i < ${#unordered[@]}; _i++ )); do
        everyone+=("${unordered[$_i]}")
    done
    for (( _i = 0; _i < ${#everyone[@]}; _i++ )); do
        _be="${everyone[$_i]}"
        set +e
        already_scored "$IID" "$_be"
        _seen=$?
        set -e
        if [[ "$_seen" -eq 0 ]]; then
            continue
        elif [[ "$_seen" -ne 1 ]]; then
            exit "$_seen"
        fi
        pending+=("$_be")
    done
    if [[ ${#pending[@]} -eq 0 ]]; then
        continue
    fi

    echo ""
    echo "=== $IID (merged: ${BACKENDS_CSV}${UNORDERED_CSV:+,$UNORDERED_CSV}) ==="

    is_pending() {
        local _b="$1" _j
        for (( _j = 0; _j < ${#pending[@]}; _j++ )); do
            if [[ "${pending[$_j]}" == "$_b" ]]; then
                return 0
            fi
        done
        return 1
    }

    # Backends whose patch is in INST_DIR/merged_patch.<backend>.diff.
    ready=()

    note_errors() {
        local _reason="$1" _j
        for (( _j = 0; _j < ${#pending[@]}; _j++ )); do
            append_error "$IID" "$REPO" "${pending[$_j]}" "$_reason"
        done
    }

    if [[ ! -d "$REPO_DIR/.git" ]]; then
        note_errors "missing task repository"
        continue
    fi
    if [[ ! -f "$INST_DIR/base_sha" ]]; then
        note_errors "missing base_sha"
        continue
    fi
    BASE_SHA="$(read_sha "$INST_DIR/base_sha")"

    write_patch() {
        local _backend="$1" _sha="$2" _patch _rc
        # $_backend is not set until local returns, so the path is assigned after.
        _patch="$INST_DIR/merged_patch.${_backend}.diff"
        set +e
        git -C "$REPO_DIR" diff "$BASE_SHA" "$_sha" > "$_patch"
        _rc=$?
        set -e
        # git diff exits 1 when the trees differ. That is a usable patch.
        # A failure here is recorded; the caller keeps going with the other arms.
        if [[ "$_rc" -gt 1 ]]; then
            append_error "$IID" "$REPO" "$_backend" "git diff failed"
            return 0
        fi
        ready+=("$_backend")
    }

    if [[ "$USE_REFLOG" -eq 1 ]]; then
        if [[ ${#BACKEND_LIST[@]} -eq 0 ]]; then
            note_errors "reflog recovery needs BACKENDS"
            continue
        fi
        if [[ ${#unordered[@]} -ne 0 ]]; then
            note_errors "merged backend missing from BACKENDS"
            continue
        fi
        _reflog_file="$INST_DIR/reflog_merges.txt"
        set +e
        reflog_merge_shas "$REPO_DIR" > "$_reflog_file"
        _reflog_rc=$?
        set -e
        if [[ "$_reflog_rc" -ne 0 ]]; then
            note_errors "reflog failed"
            continue
        fi
        merges=()
        while IFS= read -r _sha; do
            [[ -n "$_sha" ]] && merges+=("$_sha")
        done < "$_reflog_file"
        if [[ ${#merges[@]} -ne ${#ordered[@]} ]]; then
            note_errors "reflog merges ${#merges[@]} != ${#ordered[@]} merged arms"
            continue
        fi
        for (( _i = 0; _i < ${#ordered[@]}; _i++ )); do
            _be="${ordered[$_i]}"
            if ! is_pending "$_be"; then
                continue
            fi
            write_patch "$_be" "${merges[$_i]}"
        done
    else
        for (( _i = 0; _i < ${#everyone[@]}; _i++ )); do
            _be="${everyone[$_i]}"
            if ! is_pending "$_be"; then
                continue
            fi
            _sha_file="$INST_DIR/merged_sha.$_be"
            if [[ ! -f "$_sha_file" ]]; then
                append_error "$IID" "$REPO" "$_be" "no merged_sha"
                continue
            fi
            write_patch "$_be" "$(read_sha "$_sha_file")"
        done
    fi

    if [[ ${#ready[@]} -eq 0 ]]; then
        continue
    fi

    IMAGE="$(image_name "$IID")"
    # Same set -e constraint as gate_precision_run.sh: a pipeline must not
    # hide a failing pull, and `|| true` would.
    set +e
    (
        set -euo pipefail
        echo "  docker pull $IMAGE"
        docker pull --platform linux/amd64 "$IMAGE"
    ) 2>&1 | tee -a "$INST_DIR/miss.log"
    pull_rc=${PIPESTATUS[0]}
    set -e
    if [[ "$pull_rc" -ne 0 ]]; then
        echo "  FAILED: could not pull image for $IID"
        for (( _i = 0; _i < ${#ready[@]}; _i++ )); do
            append_error "$IID" "$REPO" "${ready[$_i]}" "image pull failed"
        done
        continue
    fi

    for (( _i = 0; _i < ${#ready[@]}; _i++ )); do
        BACKEND="${ready[$_i]}"
        PREDICTIONS="$INST_DIR/merged_predictions.$BACKEND.json"
        PATCH="$INST_DIR/merged_patch.$BACKEND.diff"
        python3 - "$PATCH" "$IID" "$BACKEND" "$PREDICTIONS" <<'PY'
import json, sys
patch = open(sys.argv[1], encoding="utf-8").read()
json.dump([{
    "instance_id": sys.argv[2],
    "model_name_or_path": "aoa-merged-" + sys.argv[3],
    "model_patch": patch,
}], open(sys.argv[4], "w", encoding="utf-8"), indent=2)
PY
        HARNESS_RUN_ID="gm-${IID//\//_}-${BACKEND}-$(date +%s)"
        MODEL="aoa-merged-${BACKEND}"
        set +e
        (
            set -euo pipefail
            cd "$RUN_DIR"
            uv run --with "swebench==4.1.0" \
                python -m swebench.harness.run_evaluation \
                --predictions_path "$PREDICTIONS" \
                --run_id "$HARNESS_RUN_ID" \
                --max_workers 1 \
                --cache_level env \
                --dataset_name "princeton-nlp/SWE-bench_Lite" \
                --split test
        ) 2>&1 | tee -a "$INST_DIR/miss.$BACKEND.log"
        harness_rc=${PIPESTATUS[0]}
        set -e

        REPORT_PATH="$RUN_DIR/logs/run_evaluation/$HARNESS_RUN_ID/$MODEL/$IID/report.json"
        if [[ ! -f "$REPORT_PATH" ]]; then
            set +e
            found="$(find "$RUN_DIR/logs/run_evaluation/$HARNESS_RUN_ID" \
                -name report.json -path "*/$IID/report.json" 2>/dev/null | head -1)"
            set -e
            if [[ -n "${found:-}" ]]; then
                REPORT_PATH="$found"
            fi
        fi
        if [[ ! -f "$REPORT_PATH" ]]; then
            if [[ "$harness_rc" -ne 0 ]]; then
                append_error "$IID" "$REPO" "$BACKEND" "harness failed"
            else
                append_error "$IID" "$REPO" "$BACKEND" "report not found"
            fi
            continue
        fi

        set +e
        python3 - "$ROOT/scripts" "$REPORT_PATH" "$IID" "$REPO" "$BACKEND" "$SCORES" <<'PY'
import json, sys
from pathlib import Path

sys.path.insert(0, sys.argv[1])
from gate_miss_summary import parse_harness_report

scripts, report_path, iid, repo, backend, scores = sys.argv[1:]
try:
    report = json.loads(Path(report_path).read_text(encoding="utf-8"))
    verdict = parse_harness_report(report, iid).to_dict()
except (OSError, json.JSONDecodeError, TypeError, ValueError):
    verdict = {
        "resolved": None,
        "f2p_failed": [],
        "p2p_failed": [],
        "error": "report parse failed",
    }
row = {
    "instance_id": iid,
    "repo": repo,
    "backend": backend,
    "resolved": verdict["resolved"],
    "f2p_failed": verdict["f2p_failed"],
    "p2p_failed": verdict["p2p_failed"],
    "error": verdict["error"],
}
with open(scores, "a", encoding="utf-8") as handle:
    handle.write(json.dumps(row) + "\n")
PY
        parse_rc=$?
        set -e
        if [[ "$parse_rc" -ne 0 ]]; then
            append_error "$IID" "$REPO" "$BACKEND" "report parse failed"
        fi
    done

    set +e
    docker rmi "$IMAGE" >/dev/null 2>&1
    rmi_rc=$?
    set -e
    if [[ "$rmi_rc" -ne 0 ]]; then
        echo "  warning: docker rmi $IMAGE failed" >&2
    fi
done < "$LISTING"

rm -f "$LISTING"

echo ""
echo "=== Done ==="
echo "  scores: $SCORES"
