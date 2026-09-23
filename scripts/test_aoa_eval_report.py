"""Tests for aoa_eval_report.py, against the real shape of `aoa eval --json`."""
from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

import aoa_eval_report as r

SCRIPT = Path(__file__).resolve().parent / "aoa_eval_report.py"

# The shape `aoa eval --json` emits: a list of per-task reports whose counts
# live under "metrics", not at the top level.
MERGED = {
    "task": "pallets__flask-4992",
    "backend": "agy",
    "success": True,
    "metrics": {"merged": 1, "failed": 0, "tokens_total": 172897},
}
REJECTED = {
    "task": "psf__requests-2317",
    "backend": "agy",
    "success": False,
    "metrics": {"merged": 0, "failed": 1, "tokens_total": 90000},
    "rejected_patches": [{"ticket_id": "t1", "reason": "gate failed: go test", "diff": "--- a\n"}],
}
SANDBOX_ONLY = {
    **REJECTED,
    "rejected_patches": [
        {"ticket_id": "t1", "reason": "gate could not run (sandbox failure): x", "diff": "--- a\n"}
    ],
}
NO_CHANGE = {"task": "t", "success": False, "metrics": {"merged": 0, "failed": 1, "tokens_total": 5}}


def test_outcome_reads_metrics() -> None:
    assert r.outcome(MERGED) == "merged"
    assert r.outcome(REJECTED) == "rejected"
    assert r.outcome(SANDBOX_ONLY) == "infra"
    assert r.outcome(NO_CHANGE) == "error"
    assert r.outcome(None) == "error"


def test_tokens_reads_metrics_total() -> None:
    assert r.tokens(MERGED) == 172897
    assert r.tokens(None) == 0
    assert r.tokens({"task": "t"}) == 0


def test_find_matches_task() -> None:
    assert r.find([REJECTED, MERGED], "pallets__flask-4992") is MERGED
    assert r.find([REJECTED], "missing") is None


def test_cli(tmp_path: Path) -> None:
    report = tmp_path / "report.json"
    report.write_text(json.dumps([MERGED, REJECTED]))

    def run(*args: str) -> str:
        out = subprocess.run(
            [sys.executable, str(SCRIPT), *args], capture_output=True, text=True, check=True
        )
        return out.stdout.strip()

    assert run("outcome", str(report), "pallets__flask-4992") == "merged"
    assert run("outcome", str(report), "psf__requests-2317") == "rejected"
    assert run("tokens", str(report), "pallets__flask-4992") == "172897"
    assert run("outcome", str(report), "absent") == "error"
