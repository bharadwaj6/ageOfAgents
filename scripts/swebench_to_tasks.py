#!/usr/bin/env python3
"""Convert a SWE-bench (Lite) instances file into an `aoa eval` tasks.toml,
cloning each task's repository at its base commit.

The output feeds `aoa eval --tasks <tasks.toml> --backend claudecode` (ADR 009):
each task hands the orchestrator the issue's problem statement as the Goal.

`--gate` decides what a proposal must pass to MERGE. It is independent of the
success ORACLE (what decides "resolved"), which in the two-phase Docker eval is
always FAIL_TO_PASS scored by swebench.harness.run_evaluation:

    none   no-op Gate, every proposal merges. Measures the backend harness
           alone - aoa orchestrates but does not gate.
    f2p    the issue's FAIL_TO_PASS tests. The agent iterates against the exact
           tests that grade it; kept only to reproduce eval_swebench.sh.
    repo   the issue's PASS_TO_PASS tests - the ones already passing at
           base_commit. A regression Gate that rejects a patch breaking existing
           behaviour without naming the answer, so the oracle stays held out.

`none` vs `repo` on the same instances and backend is the A/B that isolates what
aoa's verifier-gated merge queue contributes; see docs/design/live_eval.md.

Input: a JSON array (or JSONL) of SWE-bench instances. Required fields per row:
    instance_id, repo, base_commit, problem_statement, FAIL_TO_PASS
    (PASS_TO_PASS is additionally required for --gate=repo)
Test lists may be a list or a JSON-encoded string (both dataset forms work).

Usage:
    swebench_to_tasks.py INSTANCES.json WORKDIR TASKS_OUT.toml [--limit N]
                         [--gate none|f2p|repo]

Important environment caveat (ADR 009): aoa drives the agent and runs the Gate,
but this adapter does NOT install each repo's Python dependencies. Run it inside
a prepared environment for the target repos (e.g. the official SWE-bench Docker
image, or a venv with the repo installed) so that `python -m pytest` can import
them. aoa is responsible for orchestration + verification, not env provisioning.
"""
import argparse
import json
import os
import shlex
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


# pytest node ids are passed on the command line; PASS_TO_PASS runs to 1689 ids on
# the largest Lite instance, so the Gate is split into several commands rather
# than one unbounded argv.
PYTEST_IDS_PER_COMMAND = 40

# SWE-bench images install the project editable from /testbed, so the agent's
# worktree must be mounted over that path or the Gate tests the image's copy.
SANDBOX_MOUNT = "/testbed"


def swebench_image(instance_id):
    """Official SWE-bench eval image for an instance.

    The harness escapes the `__` in an instance id as `_1776_`, e.g.
    astropy__astropy-12907 -> swebench/sweb.eval.x86_64.astropy_1776_astropy-12907.
    These are the same images `swebench.harness.run_evaluation` builds in phase 3,
    so gating in them costs no extra pulls.
    """
    return f"swebench/sweb.eval.x86_64.{instance_id.replace('__', '_1776_')}:latest"


def conda_pytest(test_ids):
    """A pytest command that runs inside a SWE-bench image's `testbed` env.

    The image installs the project editable from /testbed, so the repo is mounted
    over that path (SANDBOX_MOUNT) and the conda env must be activated before
    python resolves to the prepared interpreter.
    """
    ids = " ".join(shlex.quote(t) for t in test_ids)
    script = (
        "source /opt/miniconda3/bin/activate && conda activate testbed && "
        f"python -m pytest -q {ids}"
    )
    return ["/bin/bash", "-lc", script]


def chunked(items, n):
    """Yield successive n-sized lists from items."""
    for i in range(0, len(items), n):
        yield items[i : i + n]


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
    cache_dir = os.path.expanduser("~/.cache/aoa/swebench_repos")
    os.makedirs(cache_dir, exist_ok=True)
    repo_safe = repo.replace("/", "__")
    cache_path = os.path.join(cache_dir, repo_safe)

    if not os.path.isdir(os.path.join(cache_path, ".git")):
        git("clone", "--quiet", f"https://github.com/{repo}.git", cache_path)

    dest = os.path.join(workdir, instance_id)
    if not os.path.isdir(os.path.join(dest, ".git")):
        git("clone", "--quiet", "--local", cache_path, dest)
    git("-C", dest, "checkout", "--quiet", base_commit)
    git("-C", dest, "checkout", "-B", "main")
    return dest


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("instances", help="SWE-bench instances JSON/JSONL file")
    ap.add_argument("workdir", help="directory to clone repos into")
    ap.add_argument("out", help="tasks.toml to write")
    ap.add_argument("--limit", type=int, default=0, help="only the first N instances")
    ap.add_argument(
        "--gate", choices=("none", "f2p", "repo"), default=None,
        help=(
            "What a proposal must pass to merge. Independent of the success "
            "oracle, which is always FAIL_TO_PASS scored by the official Docker "
            "harness. 'none': no-op Gate, every proposal merges - measures the "
            "backend harness alone. 'f2p': the issue's FAIL_TO_PASS tests - the "
            "agent iterates against its own grader; kept only to reproduce the "
            "single-phase eval_swebench.sh. 'repo': the issue's PASS_TO_PASS "
            "tests - a regression Gate that rejects patches breaking existing "
            "behaviour without naming the answer. Default: f2p (historical)."
        ),
    )
    ap.add_argument(
        "--inference-mode", action="store_true",
        help="Deprecated alias for --gate=none.",
    )
    a = ap.parse_args()

    if a.gate is None:
        a.gate = "none" if a.inference_mode else "f2p"
    elif a.inference_mode and a.gate != "none":
        ap.error("--inference-mode conflicts with --gate=%s" % a.gate)

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

            success: list[list[str]] = []
            if a.gate == "none":
                # No-op Gate: agent's proposal always merges; the official Docker
                # harness (Phase 2) does the real test verification. This measures
                # the backend harness, not aoa - nothing is gated.
                gate = [["true"]]
            elif a.gate == "repo":
                # Regression Gate: the tests that already pass at base_commit. It
                # rejects a patch that breaks existing behaviour without ever
                # naming FAIL_TO_PASS, so the oracle stays held out. Chunked
                # because PASS_TO_PASS runs to hundreds of ids on some instances.
                p2p = as_list(r.get("PASS_TO_PASS", []))
                if not p2p:
                    print(f"skipping {iid}: --gate=repo needs PASS_TO_PASS tests",
                          file=sys.stderr)
                    continue
                gate = [conda_pytest(chunk)
                        for chunk in chunked(p2p, PYTEST_IDS_PER_COMMAND)]
            else:
                # Gate AND success oracle are the issue's reproduce tests: the
                # agent can iterate against the exact tests that grade it.
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
            f.write(f"success = {toml_cmd_list(success)}\n")
            if a.gate == "repo":
                f.write('sandbox = "docker"\n')
                f.write(f"sandbox_image = {toml_str(swebench_image(iid))}\n")
                f.write(f"sandbox_mount = {toml_str(SANDBOX_MOUNT)}\n")
            f.write("\n")
            written += 1
            print(f"prepared {iid} -> {dest}", file=sys.stderr)

    print(f"wrote {written} task(s) to {a.out}")


if __name__ == "__main__":
    main()
