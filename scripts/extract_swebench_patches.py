#!/usr/bin/env python3
"""Extract SWE-bench predictions from an aoa eval run.

After `aoa eval` completes in inference mode (--inference-mode), each task's
repo_dir has `main` pointing at whatever the agent merged.  This script reads
the tasks.toml (which stores base_commit per task), runs
`git diff <base_commit>..main` in each repo, and writes a SWE-bench predictions
JSON file consumable by `swebench.harness.run_evaluation`.

Usage:
    extract_swebench_patches.py TASKS.toml PREDICTIONS.json [--model NAME]
"""
import argparse
import json
import subprocess
import sys

try:
    import tomllib  # Python 3.11+
except ModuleNotFoundError:
    try:
        import tomli as tomllib  # type: ignore[no-reuse-def]
    except ModuleNotFoundError:
        sys.exit(
            "Need tomllib (Python 3.11+) or 'tomli' package. "
            "Run: uv run --with tomli extract_swebench_patches.py ..."
        )


def git_diff(repo_dir: str, base_commit: str) -> str:
    result = subprocess.run(
        ["git", "diff", f"{base_commit}..main"],
        cwd=repo_dir,
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        print(
            f"  warning: git diff failed in {repo_dir}: {result.stderr.strip()}",
            file=sys.stderr,
        )
        return ""
    return result.stdout


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("tasks", help="tasks.toml written by swebench_to_tasks.py")
    ap.add_argument("out", help="predictions JSON file to write")
    ap.add_argument(
        "--model", default="aoa-claudecode",
        help="model_name_or_path label in the predictions file (default: aoa-claudecode)",
    )
    a = ap.parse_args()

    with open(a.tasks, "rb") as f:
        data = tomllib.load(f)

    tasks = data.get("task", [])
    predictions = []
    empty = 0

    for t in tasks:
        iid = t["name"]
        repo_dir = t["repo_dir"]
        base_commit = t.get("base_commit", "")

        if not base_commit:
            print(f"  skip {iid}: no base_commit in tasks.toml", file=sys.stderr)
            empty += 1
            continue

        patch = git_diff(repo_dir, base_commit)
        if not patch:
            print(f"  {iid}: empty patch (agent produced no merged change)", file=sys.stderr)
            empty += 1

        predictions.append({
            "instance_id": iid,
            "model_name_or_path": a.model,
            "model_patch": patch,
        })
        print(f"  {iid}: {len(patch)} bytes", file=sys.stderr)

    with open(a.out, "w") as f:
        json.dump(predictions, f, indent=2)

    solved = len(predictions) - empty
    print(
        f"wrote {len(predictions)} prediction(s) to {a.out} "
        f"({solved} non-empty, {empty} empty)",
        file=sys.stderr,
    )


if __name__ == "__main__":
    main()
