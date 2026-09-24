"""Tests for gate_miss_summary.py: harness reports and Gate-miss rates."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path
from typing import Any

SCRIPT = Path(__file__).resolve().parent / "gate_miss_summary.py"

_scripts_dir = Path(__file__).resolve().parent
_repo_root = _scripts_dir.parent
for _p in (str(_repo_root), str(_scripts_dir)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

try:
    from scripts.gate_miss_summary import parse_harness_report
    from scripts.precision_summary import SCREENING_WARNING
except ModuleNotFoundError:
    from gate_miss_summary import parse_harness_report  # type: ignore[no-redef]
    from precision_summary import SCREENING_WARNING  # type: ignore[no-redef]


def _write(path: Path, rows: list[dict[str, Any]]) -> None:
    path.write_text("".join(json.dumps(row) + "\n" for row in rows), encoding="utf-8")


def _score(
    instance_id: str,
    backend: str,
    *,
    resolved: bool | None,
    p2p: list[str],
    f2p: list[str] | None = None,
    error: str | None = None,
) -> dict[str, Any]:
    return {
        "instance_id": instance_id,
        "repo": "org/app",
        "backend": backend,
        "resolved": resolved,
        "f2p_failed": f2p or [],
        "p2p_failed": p2p,
        "error": error,
    }


def test_parse_harness_report(tmp_path: Path) -> None:
    """Resolved, FAIL_TO_PASS failures and PASS_TO_PASS failures come from report.json."""
    report = {
        "proj__app-1": {
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
                    "failure": ["test_regressed", "test_also"],
                },
                "FAIL_TO_FAIL": {"success": [], "failure": []},
                "PASS_TO_FAIL": {"success": [], "failure": []},
            },
        }
    }
    path = tmp_path / "report.json"
    path.write_text(json.dumps(report), encoding="utf-8")
    verdict = parse_harness_report(json.loads(path.read_text(encoding="utf-8")), "proj__app-1")
    assert verdict.resolved is False
    assert verdict.f2p_failed == ("test_still_fails",)
    assert verdict.p2p_failed == ("test_regressed", "test_also")
    assert verdict.error is None

    # The harness omits tests_status when the patch never ran. resolved is still a verdict.
    unapplied = {
        "proj__app-1": {
            "patch_is_None": False,
            "patch_exists": True,
            "patch_successfully_applied": False,
            "resolved": True,
        }
    }
    clean = parse_harness_report(unapplied, "proj__app-1")
    assert clean.resolved is True
    assert clean.f2p_failed == ()
    assert clean.p2p_failed == ()
    assert clean.error is None

    missing = parse_harness_report(report, "other")
    assert missing.resolved is None
    assert missing.f2p_failed == ()
    assert missing.p2p_failed == ()
    assert missing.error == "report missing instance"


def test_summary_miss_count_and_interval(tmp_path: Path) -> None:
    """Gate misses are scored lines with a PASS_TO_PASS failure, with a Wilson interval."""
    path = tmp_path / "merged_scores.jsonl"
    _write(
        path,
        [
            _score("inst-ok", "agy", resolved=True, p2p=[]),
            _score("inst-miss", "agy", resolved=False, p2p=["test_keep"], f2p=["test_new"]),
            _score(
                "inst-miss-two",
                "agy",
                resolved=False,
                p2p=["test_old", "test_other"],
            ),
            _score("inst-err", "agy", resolved=None, p2p=[], error="reflog failed"),
        ],
    )

    human = subprocess.run(
        [sys.executable, str(SCRIPT), str(path)],
        capture_output=True,
        text=True,
        check=True,
    )
    assert "Scored: 3" in human.stdout
    assert "Gate misses: 2/3" in human.stdout
    assert "inst-miss: test_keep" in human.stdout
    assert "inst-miss-two: test_old, test_other" in human.stdout
    assert "inst-err: reflog failed" in human.stdout
    assert SCREENING_WARNING in human.stdout

    proc = subprocess.run(
        [sys.executable, str(SCRIPT), str(path), "--json"],
        capture_output=True,
        text=True,
        check=True,
    )
    data = json.loads(proc.stdout)
    assert data["backend"] == "agy"
    assert data["scored"] == 3
    assert data["errors"] == 1
    assert data["gate_misses"] == 2
    assert data["miss_rate"] == 0.6667
    assert data["wilson_interval"] == [0.2077, 0.9385]
    assert data["resolved"] == 1
    assert data["resolve_rate"] == 0.3333
    assert data["warning"] == SCREENING_WARNING
    assert data["misses"] == [
        {"instance_id": "inst-miss", "p2p_failed": ["test_keep"]},
        {"instance_id": "inst-miss-two", "p2p_failed": ["test_old", "test_other"]},
    ]

    wide = tmp_path / "ten.jsonl"
    rows = [
        _score(f"ok-{i}", "agy", resolved=True, p2p=[])
        for i in range(6)
    ] + [
        _score(f"miss-{i}", "agy", resolved=False, p2p=[f"test_{i}"])
        for i in range(4)
    ]
    _write(wide, rows)
    ten = subprocess.run(
        [sys.executable, str(SCRIPT), str(wide), "--json"],
        capture_output=True,
        text=True,
        check=True,
    )
    ten_data = json.loads(ten.stdout)
    assert ten_data["scored"] == 10
    assert ten_data["gate_misses"] == 4
    assert ten_data["miss_rate"] == 0.4
    assert ten_data["wilson_interval"] == [0.1682, 0.6873]
    assert ten_data["warning"] is None


def test_summary_refuses_to_pool(tmp_path: Path) -> None:
    """Two backends in one file are not pooled unless --backend names an arm."""
    path = tmp_path / "merged_scores.jsonl"
    _write(
        path,
        [
            _score("a", "agy", resolved=False, p2p=["test_keep"]),
            _score("b", "grok", resolved=True, p2p=[]),
        ],
    )
    refused = subprocess.run(
        [sys.executable, str(SCRIPT), str(path)],
        capture_output=True,
        text=True,
        check=False,
    )
    assert refused.returncode != 0
    assert "backend" in refused.stderr.lower()
    assert "agy" in refused.stderr
    assert "grok" in refused.stderr

    agy = subprocess.run(
        [sys.executable, str(SCRIPT), str(path), "--backend", "agy", "--json"],
        capture_output=True,
        text=True,
        check=True,
    )
    agy_data = json.loads(agy.stdout)
    assert agy_data["backend"] == "agy"
    assert agy_data["scored"] == 1
    assert agy_data["gate_misses"] == 1
    assert agy_data["wilson_interval"] == [0.2065, 1.0]

    grok = subprocess.run(
        [sys.executable, str(SCRIPT), str(path), "--backend", "grok", "--json"],
        capture_output=True,
        text=True,
        check=True,
    )
    grok_data = json.loads(grok.stdout)
    assert grok_data["scored"] == 1
    assert grok_data["gate_misses"] == 0
    assert grok_data["resolve_rate"] == 1.0
