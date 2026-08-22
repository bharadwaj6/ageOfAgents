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


# The Gate runs whole test files; a few instances touch many, so cap the argv.
PYTEST_FILES_PER_COMMAND = 20

# Where aoa mounts the repo inside a docker sandbox (verify.SandboxMount).
SANDBOX_MOUNT = "/workspace"


# The published per-instance images. These are x86_64-only but run fine under
# emulation; building them locally is the path that does not work on arm64 (the
# miniconda installer in the base image exits 255 under Rosetta). Neither the
# harness nor aoa pulls them, so `docker pull --platform linux/amd64 <image>` is
# a prerequisite of a --gate=repo run. Override with --sandbox-image.
DEFAULT_IMAGE_TEMPLATE = "swebench/sweb.eval.x86_64.{instance}:latest"


def swebench_image(instance_id, template):
    """Render an image template for one instance."""
    return template.format(instance=instance_id.replace("__", "_1776_"))


def conda_pytest(test_files, deselect_ids):
    """A pytest command that runs inside a SWE-bench image's `testbed` env.

    Runs whole test FILES, not PASS_TO_PASS node ids. Those ids are recorded
    against the post-test-patch tree, so ids for parametrised cases the held-out
    test patch adds do not exist at base_commit and pytest exits "not found",
    failing the gate no matter what the agent wrote. The files exist either way,
    and at base_commit they contain exactly the repo's pre-existing tests.

    FAIL_TO_PASS ids are deselected so a reproduce test that already exists is
    never part of the Gate; pytest ignores a --deselect that matches nothing, so
    the far more common case (the test patch adds it later) costs nothing.

    The image installs the project editable from /testbed and keeps compiled
    extensions there (astropy ships 17 .so files the agent's source tree does
    not), so the worktree is copied over /testbed rather than mounted onto it:
    a mount would hide those artifacts and every gate would fail on import.
    `cp -a` overlays the agent's sources while leaving the build products in
    place. The conda env must be active before python resolves to the prepared
    interpreter.
    """
    args = " ".join(shlex.quote(f) for f in test_files)
    for t in deselect_ids:
        args += f" --deselect {shlex.quote(t)}"
    script = (
        f"cp -a {SANDBOX_MOUNT}/. /testbed/ && "
        "source /opt/miniconda3/bin/activate && conda activate testbed && "
        f"cd /testbed && python -m pytest -q {args}"
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
        "--sandbox-image", default=None, metavar="TEMPLATE",
        help=(
            "Image template for the --gate=repo sandbox; '{instance}' is replaced "
            "with the harness-escaped instance id. Defaults to the published "
            "image (%s), which must be pulled first." % DEFAULT_IMAGE_TEMPLATE
        ),
    )
    ap.add_argument(
        "--max-attempts", type=int, default=0, metavar="N",
        help=(
            "Cap attempts per ticket (0 = aoa's default of 2). Pass 1 to measure "
            "Gate precision: every rejection becomes terminal, so the rejected "
            "proposal is preserved and reported instead of being retried away."
        ),
    )
    ap.add_argument(
        "--inference-mode", action="store_true",
        help="Deprecated alias for --gate=none.",
    )
    a = ap.parse_args()

    if a.sandbox_image is None:
        a.sandbox_image = DEFAULT_IMAGE_TEMPLATE

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
                files = sorted({t.split("::")[0] for t in p2p})
                gate = [conda_pytest(chunk, f2p)
                        for chunk in chunked(files, PYTEST_FILES_PER_COMMAND)]
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
            if a.max_attempts:
                f.write(f"max_attempts = {a.max_attempts}\n")
            if a.gate == "repo":
                f.write('sandbox = "docker"\n')
                f.write(f"sandbox_image = {toml_str(swebench_image(iid, a.sandbox_image))}\n")
            f.write("\n")
            written += 1
            print(f"prepared {iid} -> {dest}", file=sys.stderr)

    print(f"wrote {written} task(s) to {a.out}")


if __name__ == "__main__":
    main()
