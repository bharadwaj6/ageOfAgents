#!/usr/bin/env python3
"""Convert a SWE-bench (Lite) instances file into an `aoa eval` tasks.toml,
cloning each task's repository at its base commit.

The output feeds `aoa eval --tasks <tasks.toml> --backend claudecode` (ADR 009):
each task hands the orchestrator the issue's problem statement as the Goal, uses
the issue's FAIL_TO_PASS tests as both the merge Gate and the success oracle, and
scores whether the agent's merged change makes those tests pass.

Input: a JSON array (or JSONL) of SWE-bench instances. Required fields per row:
    instance_id, repo, base_commit, problem_statement, FAIL_TO_PASS
FAIL_TO_PASS may be a list or a JSON-encoded string (both dataset forms work).

Usage:
    swebench_to_tasks.py INSTANCES.json WORKDIR TASKS_OUT.toml [--limit N]

Important environment caveat (ADR 009): aoa drives the agent and runs the Gate,
but this adapter does NOT install each repo's Python dependencies. Run it inside
a prepared environment for the target repos (e.g. the official SWE-bench Docker
image, or a venv with the repo installed) so that `python -m pytest` can import
them. aoa is responsible for orchestration + verification, not env provisioning.
"""
import argparse
import json
import os
import subprocess
import sys


def load(path):
    with open(path) as f:
        txt = f.read().strip()
    if txt.startswith("["):
        return json.loads(txt)
    return [json.loads(line) for line in txt.splitlines() if line.strip()]


def as_list(v):
    if isinstance(v, list):
        return v
    if isinstance(v, str):
        try:
            parsed = json.loads(v)
            return parsed if isinstance(parsed, list) else [v]
        except json.JSONDecodeError:
            return [v]
    return []


def toml_str(s):
    """A TOML basic (double-quoted) string with the necessary escapes."""
    s = s.replace("\\", "\\\\").replace('"', '\\"')
    s = s.replace("\n", "\\n").replace("\r", "\\r").replace("\t", "\\t")
    return '"' + s + '"'


def toml_cmd_list(cmds):
    parts = ["[" + ", ".join(toml_str(a) for a in c) + "]" for c in cmds]
    return "[" + ", ".join(parts) + "]"


def git(*args, check=True):
    return subprocess.run(["git", *args], check=check,
                          stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)


def prepare_repo(workdir, instance_id, repo, base_commit):
    """Clone repo at base_commit and normalize to a `main` branch.

    aoa's worktree layer cuts every worker branch from `main` and keeps `main`
    the always-green integration branch (worktree.go). SWE-bench repos use
    `master`/detached HEADs, so we point a `main` branch at the base commit and
    check it out — making aoa's assumptions hold without touching aoa.
    """
    dest = os.path.join(workdir, instance_id)
    if not os.path.isdir(os.path.join(dest, ".git")):
        git("clone", "--quiet", f"https://github.com/{repo}.git", dest)
    git("-C", dest, "checkout", "--quiet", base_commit)
    git("-C", dest, "checkout", "-B", "main")  # main == base_commit, checked out
    return dest


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("instances", help="SWE-bench instances JSON/JSONL file")
    ap.add_argument("workdir", help="directory to clone repos into")
    ap.add_argument("out", help="tasks.toml to write")
    ap.add_argument("--limit", type=int, default=0, help="only the first N instances")
    ap.add_argument(
        "--inference-mode", action="store_true",
        help=(
            "Use a no-op Gate (always passes) so the agent can merge without a "
            "prepared test environment. Intended for Phase 1 of the two-phase "
            "Docker eval: aoa generates patches here; the official "
            "swebench.harness.run_evaluation harness scores them in Docker later."
        ),
    )
    a = ap.parse_args()

    rows = load(a.instances)
    if a.limit:
        rows = rows[: a.limit]
    os.makedirs(a.workdir, exist_ok=True)

    written = 0
    with open(a.out, "w") as f:
        for r in rows:
            iid = r["instance_id"]
            base_commit = r["base_commit"]
            dest = prepare_repo(a.workdir, iid, r["repo"], base_commit)
            f2p = as_list(r.get("FAIL_TO_PASS", []))

            if a.inference_mode:
                # No-op Gate: agent's proposal always merges; the official Docker
                # harness (Phase 2) does the real test verification.
                gate = [["true"]]
                success: list[list[str]] = []
            else:
                # Gate AND success oracle are the issue's reproduce tests.
                if f2p:
                    gate = [["python", "-m", "pytest", "-q", t] for t in f2p]
                else:
                    gate = [["python", "-m", "pytest", "-q"]]
                success = gate

            f.write("[[task]]\n")
            f.write(f"name = {toml_str(iid)}\n")
            f.write(f"repo_dir = {toml_str(dest)}\n")
            # base_commit is read by extract_swebench_patches.py; aoa ignores it.
            f.write(f"base_commit = {toml_str(base_commit)}\n")
            f.write(f"goal = {toml_str(r.get('problem_statement', '').strip())}\n")
            f.write(f"gate = {toml_cmd_list(gate)}\n")
            f.write(f"success = {toml_cmd_list(success)}\n\n")
            written += 1
            print(f"prepared {iid} -> {dest}", file=sys.stderr)

    print(f"wrote {written} task(s) to {a.out}")


if __name__ == "__main__":
    main()
