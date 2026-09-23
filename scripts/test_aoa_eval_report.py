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


# ── new: per-backend stop count ──────────────────────────────────────────────


def _results_line(
    instance_id: str,
    backend: str,
    outcome: str,
    gate_valid: bool | None,
) -> str:
    """Return a JSON line matching the results.jsonl schema."""
    return json.dumps(
        {
            "instance_id": instance_id,
            "repo": "r",
            "backend": backend,
            "outcome": outcome,
            "gate_valid": gate_valid,
            "oracle": None,
            "tokens": 1,
            "seconds": 1.0,
        }
    )


def test_count_gate_valid_rejections_per_backend_empty(tmp_path: Path) -> None:
    """Returns empty dict when the file does not exist."""
    counts = r.count_gate_valid_rejections_per_backend(tmp_path / "missing.jsonl")
    assert counts == {}


def test_count_gate_valid_rejections_per_backend_two_backends(tmp_path: Path) -> None:
    """Counts gate-valid rejections per backend correctly on a two-backend results.jsonl."""
    lines = [
        # agy: 2 gate-valid rejections
        _results_line("i1", "agy", "rejected", True),
        _results_line("i2", "agy", "rejected", True),
        # agy: not gate-valid -> does not count
        _results_line("i3", "agy", "rejected", False),
        # agy: merged -> does not count
        _results_line("i4", "agy", "merged", None),
        # grok: 1 gate-valid rejection
        _results_line("i1", "grok", "rejected", True),
        # grok: gate_valid None -> does not count
        _results_line("i2", "grok", "rejected", None),
    ]
    path = tmp_path / "results.jsonl"
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")

    counts = r.count_gate_valid_rejections_per_backend(path)
    assert counts.get("agy") == 2
    assert counts.get("grok") == 1
    # Backends with zero rejections are not present
    assert "mock" not in counts


def test_count_gate_valid_rejections_per_backend_no_backend_field(tmp_path: Path) -> None:
    """Lines without a backend field are counted under the empty string key."""
    line = json.dumps(
        {
            "instance_id": "i1",
            "repo": "r",
            "outcome": "rejected",
            "gate_valid": True,
            "oracle": None,
            "tokens": 1,
            "seconds": 1.0,
        }
    )
    path = tmp_path / "results.jsonl"
    path.write_text(line + "\n", encoding="utf-8")

    counts = r.count_gate_valid_rejections_per_backend(path)
    assert counts.get("") == 1
