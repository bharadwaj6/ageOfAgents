"""The precision runner resets every eval to the prepared base.

These drive scripts/gate_precision_run.sh with stub docker, go, uv and aoa
binaries so the reset behaviour can be checked without a build or an image.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
from pathlib import Path

SCRIPTS = Path(__file__).resolve().parent
IID = "proj__app-1"

GO_STUB = """#!/usr/bin/env bash
set -euo pipefail
out="aoa"
prev=""
for arg in "$@"; do
    if [[ "$prev" == "-o" ]]; then
        out="$arg"
    fi
    prev="$arg"
done
cp "$AOA_STUB_BIN" "$out"
chmod +x "$out"
"""

UV_NOOP = """#!/usr/bin/env bash
exit 0
"""

# Prepares the one repo swebench_to_tasks.py would have cloned, without network.
UV_PREPARE = """#!/usr/bin/env python3
from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path


def main() -> None:
    args = sys.argv[1:]
    script = next((a for a in args if a.endswith("swebench_to_tasks.py")), "")
    if not script:
        return
    idx = args.index(script)
    one_inst = Path(args[idx + 1])
    repos_dir = Path(args[idx + 2])
    tasks = Path(args[idx + 3])
    iid = json.loads(one_inst.read_text())[0]["instance_id"]
    repo = repos_dir / iid
    repo.mkdir(parents=True)
    subprocess.run(
        ["git", "init", "-b", "main", str(repo)],
        check=True,
        stdout=subprocess.DEVNULL,
    )
    for key, value in (
        ("user.email", "test@example.com"),
        ("user.name", "Test"),
        ("commit.gpgsign", "false"),
    ):
        subprocess.run(
            ["git", "-C", str(repo), "config", key, value],
            check=True,
            stdout=subprocess.DEVNULL,
        )
    (repo / "README").write_text("base\\n")
    subprocess.run(
        ["git", "-C", str(repo), "add", "README"],
        check=True,
        stdout=subprocess.DEVNULL,
    )
    subprocess.run(
        ["git", "-C", str(repo), "commit", "-m", "base"],
        check=True,
        stdout=subprocess.DEVNULL,
    )
    tasks.write_text('name = "' + iid + '"\\n')


if __name__ == "__main__":
    main()
"""

_AOA_BACKEND_PARSE = """backend=""
prev=""
for arg in "$@"; do
    if [[ "$prev" == "--backend" ]]; then
        backend="$arg"
    fi
    prev="$arg"
done
sha="$(git -C "$AOA_STUB_REPO" rev-parse HEAD)"
printf '%s %s\\n' "$backend" "$sha" >> "$AOA_STUB_LOG"
"""

AOA_MERGED = (
    """#!/usr/bin/env bash
set -euo pipefail
"""
    + _AOA_BACKEND_PARSE
    + """python3 -c 'import json, os
print(json.dumps([{
    "task": os.environ["AOA_STUB_TASK"],
    "metrics": {"merged": 1, "tokens_total": 3},
}]))'
"""
)

# "bad" exits before writing a report. That arm is error; the other arm is the
# report it wrote.
AOA_FAILS_ONE = (
    """#!/usr/bin/env bash
set -euo pipefail
"""
    + _AOA_BACKEND_PARSE
    + """if [[ "$backend" == "bad" ]]; then
    exit 1
fi
python3 -c 'import json, os
print(json.dumps([{
    "task": os.environ["AOA_STUB_TASK"],
    "metrics": {"merged": 1, "tokens_total": 3},
}]))'
"""
)

# Records the HEAD it was given, then merges an empty commit the way aoa would,
# so the next eval is wrong unless the runner resets again. "bad" is rejected;
# every other backend, including the Gate-validity mock, merges.
AOA_RECORDS_AND_MERGES = (
    """#!/usr/bin/env bash
set -euo pipefail
"""
    + _AOA_BACKEND_PARSE
    + """git -C "$AOA_STUB_REPO" commit --allow-empty -m "eval $backend" >/dev/null
merged=1
if [[ "$backend" == "bad" ]]; then
    merged=0
fi
python3 -c 'import json, os, sys
merged = int(sys.argv[1])
body = {
    "task": os.environ["AOA_STUB_TASK"],
    "metrics": {"merged": merged, "tokens_total": 3},
}
if not merged:
    body["metrics"]["failed"] = 1
    body["rejected_patches"] = [{"reason": "gate failed"}]
print(json.dumps([body]))' "$merged"
"""
)

DOCKER_STUB = """#!/usr/bin/env bash
printf '%s\\n' "$*" >> "$DOCKER_LOG"
exit 0
"""

# Every docker command fails. The pull is the one the runner must honour.
DOCKER_FAILS = """#!/usr/bin/env bash
printf '%s\\n' "$*" >> "$DOCKER_LOG"
exit 1
"""


def _exe(path: Path, body: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(body)
    path.chmod(0o755)


def _git(repo: Path, *args: str) -> str:
    result = subprocess.run(
        ["git", "-C", str(repo), *args],
        check=True,
        capture_output=True,
        text=True,
    )
    return result.stdout


def _init_repo(repo: Path) -> str:
    repo.mkdir(parents=True)
    subprocess.run(
        ["git", "init", "-b", "main", str(repo)],
        check=True,
        capture_output=True,
        text=True,
    )
    for key, value in (
        ("user.email", "test@example.com"),
        ("user.name", "Test"),
        ("commit.gpgsign", "false"),
    ):
        _git(repo, "config", key, value)
    (repo / "README").write_text("base\n")
    subprocess.run(
        ["git", "-C", str(repo), "add", "README"],
        check=True,
        capture_output=True,
        text=True,
    )
    subprocess.run(
        ["git", "-C", str(repo), "commit", "-m", "base"],
        check=True,
        capture_output=True,
        text=True,
    )
    return _git(repo, "rev-parse", "HEAD").strip()


def _stage(tmp_path: Path) -> Path:
    root = tmp_path / "proj"
    scripts = root / "scripts"
    scripts.mkdir(parents=True)
    for name in (
        "gate_precision_run.sh",
        "gate_precision_reset.sh",
        "aoa_eval_report.py",
    ):
        dest = scripts / name
        shutil.copy(SCRIPTS / name, dest)
        if name.endswith(".sh"):
            dest.chmod(0o755)
    return root


def _install_stubs(
    tmp_path: Path,
    aoa_body: str,
    uv_body: str,
    docker_body: str = DOCKER_STUB,
) -> Path:
    bindir = tmp_path / "bin"
    _exe(tmp_path / "aoa-stub", aoa_body)
    _exe(bindir / "go", GO_STUB)
    _exe(bindir / "uv", uv_body)
    _exe(bindir / "docker", docker_body)
    _exe(bindir / "jq", "#!/usr/bin/env bash\nexit 0\n")
    return bindir


def _env(
    tmp_path: Path,
    run_dir: Path,
    repo: Path,
    bindir: Path,
    backends: str,
) -> dict[str, str]:
    env = os.environ.copy()
    env["PATH"] = str(bindir) + os.pathsep + env.get("PATH", "")
    env["RUN_DIR"] = str(run_dir)
    env["BACKENDS"] = backends
    env["AOA_STUB_REPO"] = str(repo)
    env["AOA_STUB_LOG"] = str(tmp_path / "aoa.log")
    env["AOA_STUB_TASK"] = IID
    env["AOA_STUB_BIN"] = str(tmp_path / "aoa-stub")
    env["DOCKER_LOG"] = str(tmp_path / "docker.log")
    env.pop("DRY_RUN", None)
    return env


def _instances(tmp_path: Path) -> Path:
    path = tmp_path / "instances.json"
    path.write_text(json.dumps([{"instance_id": IID, "repo": "org/app"}]))
    return path


def _plan(run_dir: Path) -> None:
    run_dir.mkdir(parents=True, exist_ok=True)
    (run_dir / "plan.json").write_text(json.dumps([IID]))


def _run(
    root: Path, instances: Path, env: dict[str, str]
) -> subprocess.CompletedProcess[str]:
    script = root / "scripts" / "gate_precision_run.sh"
    return subprocess.run(
        ["bash", str(script), str(instances)],
        env=env,
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )


def _results(run_dir: Path) -> list[dict[str, object]]:
    path = run_dir / "results.jsonl"
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]


def _aoa_log(tmp_path: Path) -> list[str]:
    path = tmp_path / "aoa.log"
    if not path.exists():
        return []
    return [line for line in path.read_text().splitlines() if line.strip()]


def test_runner_resets_before_each_eval(tmp_path: Path) -> None:
    root = _stage(tmp_path)
    run_dir = tmp_path / "run"
    repo = run_dir / "instances" / IID / "repos" / IID
    base = _init_repo(repo)
    (repo / "README").write_text("later\n")
    subprocess.run(
        ["git", "-C", str(repo), "commit", "-am", "later"],
        check=True,
        capture_output=True,
        text=True,
    )
    inst = run_dir / "instances" / IID
    (inst / "tasks.toml").write_text(f'name = "{IID}"\n')
    (inst / "base_sha").write_text(base + "\n")
    _plan(run_dir)
    bindir = _install_stubs(tmp_path, AOA_RECORDS_AND_MERGES, UV_NOOP)
    env = _env(tmp_path, run_dir, repo, bindir, "bad other")

    proc = _run(root, _instances(tmp_path), env)

    detail = proc.stdout[-2000:] + proc.stderr[-2000:]
    assert proc.returncode == 0, detail
    seen = [line.split() for line in _aoa_log(tmp_path)]
    assert [backend for backend, _sha in seen] == ["bad", "mock", "other"], detail
    assert [sha for _backend, sha in seen] == [base, base, base], detail
    # Recording the base is once. Later merges must not replace the file.
    assert (inst / "base_sha").read_text().strip() == base
    rows = {str(row["backend"]): row for row in _results(run_dir)}
    assert rows["bad"]["outcome"] == "rejected"
    assert rows["bad"]["gate_valid"] is True
    assert rows["other"]["outcome"] == "merged"


def test_runner_records_base_sha_after_prepare(tmp_path: Path) -> None:
    root = _stage(tmp_path)
    run_dir = tmp_path / "run"
    repo = run_dir / "instances" / IID / "repos" / IID
    _plan(run_dir)
    bindir = _install_stubs(tmp_path, AOA_MERGED, UV_PREPARE)
    env = _env(tmp_path, run_dir, repo, bindir, "ok")

    proc = _run(root, _instances(tmp_path), env)

    detail = proc.stdout[-2000:] + proc.stderr[-2000:]
    assert proc.returncode == 0, detail
    recorded = (run_dir / "instances" / IID / "base_sha").read_text().strip()
    assert recorded == _git(repo, "rev-parse", "HEAD").strip()
    assert _aoa_log(tmp_path) == [f"ok {recorded}"]
    assert _results(run_dir)[0]["outcome"] == "merged"


def test_runner_records_error_when_eval_fails(tmp_path: Path) -> None:
    root = _stage(tmp_path)
    run_dir = tmp_path / "run"
    repo = run_dir / "instances" / IID / "repos" / IID
    base = _init_repo(repo)
    inst = run_dir / "instances" / IID
    (inst / "tasks.toml").write_text(f'name = "{IID}"\n')
    (inst / "base_sha").write_text(base + "\n")
    _plan(run_dir)
    bindir = _install_stubs(tmp_path, AOA_FAILS_ONE, UV_NOOP)
    env = _env(tmp_path, run_dir, repo, bindir, "bad other")

    proc = _run(root, _instances(tmp_path), env)

    detail = proc.stdout[-2000:] + proc.stderr[-2000:]
    assert proc.returncode == 0, detail
    seen = [line.split()[0] for line in _aoa_log(tmp_path)]
    assert seen == ["bad", "other"], detail
    rows = {str(row["backend"]): row for row in _results(run_dir)}
    assert rows["bad"]["outcome"] == "error"
    assert rows["other"]["outcome"] == "merged"
    assert rows["other"]["tokens"] == 3


def test_runner_records_error_when_reset_fails(tmp_path: Path) -> None:
    root = _stage(tmp_path)
    run_dir = tmp_path / "run"
    repo = run_dir / "instances" / IID / "repos" / IID
    _init_repo(repo)
    inst = run_dir / "instances" / IID
    (inst / "tasks.toml").write_text(f'name = "{IID}"\n')
    (inst / "base_sha").write_text("0123456789abcdef0123456789abcdef01234567\n")
    _plan(run_dir)
    bindir = _install_stubs(tmp_path, AOA_MERGED, UV_NOOP)
    env = _env(tmp_path, run_dir, repo, bindir, "bad other")

    proc = _run(root, _instances(tmp_path), env)

    detail = proc.stdout[-2000:] + proc.stderr[-2000:]
    assert proc.returncode == 0, detail
    assert _aoa_log(tmp_path) == [], detail
    rows = _results(run_dir)
    assert [row["backend"] for row in rows] == ["bad", "other"]
    assert [row["outcome"] for row in rows] == ["error", "error"]


def test_runner_records_error_when_pull_fails(tmp_path: Path) -> None:
    root = _stage(tmp_path)
    run_dir = tmp_path / "run"
    repo = run_dir / "instances" / IID / "repos" / IID
    base = _init_repo(repo)
    inst = run_dir / "instances" / IID
    (inst / "tasks.toml").write_text(f'name = "{IID}"\n')
    (inst / "base_sha").write_text(base + "\n")
    _plan(run_dir)
    bindir = _install_stubs(tmp_path, AOA_MERGED, UV_NOOP, DOCKER_FAILS)
    env = _env(tmp_path, run_dir, repo, bindir, "bad other")

    proc = _run(root, _instances(tmp_path), env)

    detail = proc.stdout[-2000:] + proc.stderr[-2000:]
    assert proc.returncode == 0, detail
    assert _aoa_log(tmp_path) == [], detail
    assert not (inst / "image_pulled").exists()
    rows = _results(run_dir)
    assert [row["backend"] for row in rows] == ["bad", "other"]
    assert [row["outcome"] for row in rows] == ["error", "error"]


def test_runner_records_merged_sha_before_reset(tmp_path: Path) -> None:
    """A merged arm's main commit is saved before the next arm resets it."""
    root = _stage(tmp_path)
    run_dir = tmp_path / "run"
    repo = run_dir / "instances" / IID / "repos" / IID
    base = _init_repo(repo)
    inst = run_dir / "instances" / IID
    (inst / "tasks.toml").write_text(f'name = "{IID}"\n')
    (inst / "base_sha").write_text(base + "\n")
    _plan(run_dir)
    bindir = _install_stubs(tmp_path, AOA_RECORDS_AND_MERGES, UV_NOOP)
    env = _env(tmp_path, run_dir, repo, bindir, "one two")

    proc = _run(root, _instances(tmp_path), env)

    detail = proc.stdout[-2000:] + proc.stderr[-2000:]
    assert proc.returncode == 0, detail
    one = (inst / "merged_sha.one").read_text().strip()
    two = (inst / "merged_sha.two").read_text().strip()
    assert one != base
    assert two != base
    assert one != two
    assert _git(repo, "log", "-1", "--format=%s", one).strip() == "eval one"
    assert _git(repo, "log", "-1", "--format=%s", two).strip() == "eval two"
    assert not (inst / "merged_sha.mock").exists()
    rows = {str(row["backend"]): row for row in _results(run_dir)}
    assert rows["one"]["outcome"] == "merged"
    assert rows["two"]["outcome"] == "merged"


def test_runner_errors_when_resumed_without_base_sha(tmp_path: Path) -> None:
    root = _stage(tmp_path)
    run_dir = tmp_path / "run"
    repo = run_dir / "instances" / IID / "repos" / IID
    _init_repo(repo)
    inst = run_dir / "instances" / IID
    (inst / "tasks.toml").write_text(f'name = "{IID}"\n')
    _plan(run_dir)
    bindir = _install_stubs(tmp_path, AOA_MERGED, UV_NOOP)
    env = _env(tmp_path, run_dir, repo, bindir, "bad other")

    proc = _run(root, _instances(tmp_path), env)

    detail = proc.stdout[-2000:] + proc.stderr[-2000:]
    assert proc.returncode == 0, detail
    assert _aoa_log(tmp_path) == []
    docker_log = tmp_path / "docker.log"
    assert not docker_log.exists() or docker_log.read_text() == ""
    rows = _results(run_dir)
    assert [row["backend"] for row in rows] == ["bad", "other"]
    assert [row["outcome"] for row in rows] == ["error", "error"]
    assert "base_sha" in proc.stdout
