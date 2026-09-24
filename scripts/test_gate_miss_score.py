"""Score merged Gate arms with a stubbed harness.

These drive scripts/gate_miss_score.sh with stub docker and uv binaries, the
same way scripts/test_gate_precision_run.py drives the precision runner.
"""

from __future__ import annotations

import json
import os
import subprocess
from pathlib import Path

SCRIPT = Path(__file__).resolve().parent / "gate_miss_score.sh"
IID = "proj__app-1"

DOCKER_STUB = """#!/usr/bin/env bash
printf '%s\\n' "$*" >> "$DOCKER_LOG"
exit 0
"""

# The harness stand-in writes a report.json in the shape swebench 4.1.0 writes,
# under logs/run_evaluation/<run_id>/<model>/<instance>/report.json.
UV_STUB = """#!/usr/bin/env python3
from __future__ import annotations

import json
import sys
from pathlib import Path


def flag(name: str) -> str:
    if name not in sys.argv:
        return ""
    return sys.argv[sys.argv.index(name) + 1]


def main() -> None:
    preds_path = flag("--predictions_path")
    run_id = flag("--run_id")
    log = Path(os.environ["UV_LOG"])
    with log.open("a", encoding="utf-8") as handle:
        handle.write(preds_path + "\\n")
    if not preds_path or not run_id:
        return
    preds = json.loads(Path(preds_path).read_text(encoding="utf-8"))
    pred = preds[0]
    iid = pred["instance_id"]
    model = pred["model_name_or_path"].replace("/", "__")
    out = Path("logs") / "run_evaluation" / run_id / model / iid / "report.json"
    out.parent.mkdir(parents=True, exist_ok=True)
    report = {
        iid: {
            "patch_is_None": False,
            "patch_exists": True,
            "patch_successfully_applied": True,
            "resolved": False,
            "tests_status": {
                "FAIL_TO_PASS": {
                    "success": ["test_fixed"],
                    "failure": ["test_still_fails"],
                },
                "PASS_TO_PASS": {
                    "success": ["test_ok"],
                    "failure": ["test_regressed"],
                },
                "FAIL_TO_FAIL": {"success": [], "failure": []},
                "PASS_TO_FAIL": {"success": [], "failure": []},
            },
        }
    }
    out.write_text(json.dumps(report), encoding="utf-8")


if __name__ == "__main__":
    import os

    main()
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
        ("core.logAllRefUpdates", "true"),
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


def _merge(repo: Path, branch: str, filename: str, content: str) -> str:
    """Merge a one-file branch into main and return the merge commit."""
    _git(repo, "checkout", "-b", branch)
    (repo / filename).write_text(content)
    subprocess.run(
        ["git", "-C", str(repo), "add", filename],
        check=True,
        capture_output=True,
        text=True,
    )
    subprocess.run(
        ["git", "-C", str(repo), "commit", "-m", branch],
        check=True,
        capture_output=True,
        text=True,
    )
    _git(repo, "checkout", "main")
    _git(repo, "merge", "--no-ff", "--no-edit", "-m", f"merge {branch}", branch)
    return _git(repo, "rev-parse", "HEAD").strip()


def _env(tmp_path: Path, bindir: Path) -> dict[str, str]:
    env = os.environ.copy()
    env["PATH"] = str(bindir) + os.pathsep + env.get("PATH", "")
    env["DOCKER_LOG"] = str(tmp_path / "docker.log")
    env["UV_LOG"] = str(tmp_path / "uv.log")
    env["GIT_MERGE_AUTOEDIT"] = "no"
    return env


def _install(tmp_path: Path) -> Path:
    bindir = tmp_path / "bin"
    _exe(bindir / "docker", DOCKER_STUB)
    _exe(bindir / "uv", UV_STUB)
    return bindir


def _results(run_dir: Path, rows: list[dict[str, object]]) -> None:
    (run_dir / "results.jsonl").write_text(
        "".join(json.dumps(row) + "\n" for row in rows),
        encoding="utf-8",
    )


def _row(backend: str, outcome: str) -> dict[str, object]:
    return {
        "instance_id": IID,
        "repo": "org/app",
        "backend": backend,
        "outcome": outcome,
    }


def _run(run_dir: Path, env: dict[str, str], *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["bash", str(SCRIPT), str(run_dir), *args],
        env=env,
        capture_output=True,
        text=True,
        check=False,
        timeout=30,
    )


def _scores(run_dir: Path) -> list[dict[str, object]]:
    path = run_dir / "merged_scores.jsonl"
    return [json.loads(line) for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]


def test_reflog_maps_merges_to_backends_in_order(tmp_path: Path) -> None:
    """Two merges separated by a reset map to the two backends, oldest first."""
    run_dir = tmp_path / "run"
    repo = run_dir / "instances" / IID / "repos" / IID
    base = _init_repo(repo)
    _merge(repo, "arm-agy", "a.txt", "a\n")
    _git(repo, "reset", "--hard", base)
    _merge(repo, "arm-grok", "b.txt", "b\n")
    inst = run_dir / "instances" / IID
    (inst / "base_sha").write_text(base + "\n")
    _results(
        run_dir,
        [_row("agy", "merged"), _row("bad", "rejected"), _row("grok", "merged")],
    )
    env = _env(tmp_path, _install(tmp_path))

    proc = _run(run_dir, env, "agy grok")

    detail = proc.stdout[-2000:] + proc.stderr[-2000:]
    assert proc.returncode == 0, detail
    agy = json.loads((inst / "merged_predictions.agy.json").read_text(encoding="utf-8"))
    grok = json.loads((inst / "merged_predictions.grok.json").read_text(encoding="utf-8"))
    assert agy[0]["model_name_or_path"] == "aoa-merged-agy"
    assert "a.txt" in agy[0]["model_patch"]
    assert "b.txt" not in agy[0]["model_patch"]
    assert grok[0]["model_name_or_path"] == "aoa-merged-grok"
    assert "b.txt" in grok[0]["model_patch"]
    assert "a.txt" not in grok[0]["model_patch"]

    rows = _scores(run_dir)
    assert [row["backend"] for row in rows] == ["agy", "grok"]
    for row in rows:
        assert row["instance_id"] == IID
        assert row["repo"] == "org/app"
        assert row["resolved"] is False
        assert row["f2p_failed"] == ["test_still_fails"]
        assert row["p2p_failed"] == ["test_regressed"]
        assert row["error"] is None
    assert "bad" not in {row["backend"] for row in rows}

    docker_log = (tmp_path / "docker.log").read_text(encoding="utf-8").splitlines()
    assert sum(1 for line in docker_log if line.startswith("pull ")) == 1
    assert any("proj_1776_app-1" in line for line in docker_log)
    assert sum(1 for line in docker_log if line.startswith("rmi ")) == 1

    again = _run(run_dir, env, "agy grok")
    assert again.returncode == 0, again.stdout[-1000:] + again.stderr[-1000:]
    assert len(_scores(run_dir)) == 2
    docker_again = (tmp_path / "docker.log").read_text(encoding="utf-8").splitlines()
    assert sum(1 for line in docker_again if line.startswith("pull ")) == 1


def test_reflog_one_merge_for_two_arms_is_error(tmp_path: Path) -> None:
    """A reflog with one merge cannot be assigned to two merged arms."""
    run_dir = tmp_path / "run"
    repo = run_dir / "instances" / IID / "repos" / IID
    base = _init_repo(repo)
    _merge(repo, "arm-agy", "a.txt", "a\n")
    inst = run_dir / "instances" / IID
    (inst / "base_sha").write_text(base + "\n")
    _results(run_dir, [_row("agy", "merged"), _row("grok", "merged")])
    env = _env(tmp_path, _install(tmp_path))

    proc = _run(run_dir, env, "agy", "grok")

    detail = proc.stdout[-2000:] + proc.stderr[-2000:]
    assert proc.returncode == 0, detail
    rows = {str(row["backend"]): row for row in _scores(run_dir)}
    assert set(rows) == {"agy", "grok"}
    for row in rows.values():
        assert row["resolved"] is None
        assert row["f2p_failed"] == []
        assert row["p2p_failed"] == []
        assert row["error"] == "reflog merges 1 != 2 merged arms"
    assert not (inst / "merged_predictions.agy.json").exists()
    assert not (inst / "merged_predictions.grok.json").exists()
    docker_log = tmp_path / "docker.log"
    text = docker_log.read_text(encoding="utf-8") if docker_log.exists() else ""
    assert "pull " not in text


def test_score_parses_report_from_merged_sha(tmp_path: Path) -> None:
    """A recorded merged_sha is diffed and the harness report fills the score line."""
    run_dir = tmp_path / "run"
    repo = run_dir / "instances" / IID / "repos" / IID
    base = _init_repo(repo)
    (repo / "a.txt").write_text("a\n")
    subprocess.run(
        ["git", "-C", str(repo), "add", "a.txt"],
        check=True,
        capture_output=True,
        text=True,
    )
    subprocess.run(
        ["git", "-C", str(repo), "commit", "-m", "change"],
        check=True,
        capture_output=True,
        text=True,
    )
    merged = _git(repo, "rev-parse", "HEAD").strip()
    inst = run_dir / "instances" / IID
    (inst / "base_sha").write_text(base + "\n")
    (inst / "merged_sha.agy").write_text(merged + "\n")
    _results(run_dir, [_row("agy", "merged")])
    env = _env(tmp_path, _install(tmp_path))

    proc = _run(run_dir, env)

    detail = proc.stdout[-2000:] + proc.stderr[-2000:]
    assert proc.returncode == 0, detail
    pred = json.loads((inst / "merged_predictions.agy.json").read_text(encoding="utf-8"))
    assert pred[0]["model_name_or_path"] == "aoa-merged-agy"
    assert "a.txt" in pred[0]["model_patch"]
    row = _scores(run_dir)[0]
    assert row["backend"] == "agy"
    assert row["resolved"] is False
    assert row["f2p_failed"] == ["test_still_fails"]
    assert row["p2p_failed"] == ["test_regressed"]
    assert row["error"] is None
