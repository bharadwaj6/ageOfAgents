"""Tests for scripts/precision_summary.py."""

import json
import subprocess
import sys
from pathlib import Path
from typing import Any

import pytest

SCRIPT = Path(__file__).resolve().parent / "precision_summary.py"

_scripts_dir = Path(__file__).resolve().parent
_repo_root = _scripts_dir.parent
for _p in (str(_repo_root), str(_scripts_dir)):
    if _p not in sys.path:
        sys.path.insert(0, _p)

try:
    from scripts.precision_summary import (
        SCREENING_WARNING,
        load_results,
        summarize_results,
        wilson_score_interval,
    )
except ModuleNotFoundError:
    from precision_summary import (  # type: ignore[no-redef]
        SCREENING_WARNING,
        load_results,
        summarize_results,
        wilson_score_interval,
    )


def test_wilson_interval_pinned_values() -> None:
    """Pin the Wilson score 95% interval to the five exact test values."""
    cases = [
        (8, 10, (0.4902, 0.9433)),
        (20, 20, (0.8389, 1.0)),
        (0, 5, (0.0, 0.4345)),
        (27, 30, (0.7438, 0.9654)),
        (1, 2, (0.0945, 0.9055)),
    ]
    for k, n, expected in cases:
        assert wilson_score_interval(k, n) == expected


def test_wilson_interval_zero_and_invalid() -> None:
    """With zero scored rejections, interval is None. Invalid k raises ValueError."""
    assert wilson_score_interval(0, 0) is None
    with pytest.raises(ValueError):
        wilson_score_interval(-1, 5)
    with pytest.raises(ValueError):
        wilson_score_interval(6, 5)


def test_exclusion_rules_on_tmp_path(tmp_path: Path) -> None:
    """Verify exclusion accounting, precision, per-repo counts, and warning on results."""
    records = [
        {
            "instance_id": "inst-merged",
            "repo": "django/django",
            "outcome": "merged",
            "gate_valid": None,
            "oracle": None,
            "tokens": 1000,
            "seconds": 50.0,
        },
        {
            "instance_id": "inst-unres",
            "repo": "django/django",
            "outcome": "rejected",
            "gate_valid": True,
            "oracle": "unresolved",
            "tokens": 1200,
            "seconds": 60.0,
        },
        {
            "instance_id": "inst-res",
            "repo": "django/django",
            "outcome": "rejected",
            "gate_valid": True,
            "oracle": "resolved",
            "tokens": 1100,
            "seconds": 55.0,
        },
        {
            "instance_id": "inst-infra",
            "repo": "astropy/astropy",
            "outcome": "infra",
            "gate_valid": None,
            "oracle": None,
            "tokens": 100,
            "seconds": 5.0,
        },
        {
            "instance_id": "inst-error",
            "repo": "astropy/astropy",
            "outcome": "error",
            "gate_valid": None,
            "oracle": None,
            "tokens": 100,
            "seconds": 5.0,
        },
        {
            "instance_id": "inst-gv-false",
            "repo": "astropy/astropy",
            "outcome": "rejected",
            "gate_valid": False,
            "oracle": "unresolved",
            "tokens": 800,
            "seconds": 40.0,
        },
        {
            "instance_id": "inst-oracle-err",
            "repo": "astropy/astropy",
            "outcome": "rejected",
            "gate_valid": True,
            "oracle": "error",
            "tokens": 900,
            "seconds": 45.0,
        },
    ]

    results_file = tmp_path / "results.jsonl"
    with results_file.open("w", encoding="utf-8") as f:
        for r in records:
            f.write(json.dumps(r) + "\n")

    results = load_results(results_file)
    summary = summarize_results(results)

    # Total instances and outcome counts
    assert summary.total_instances == 7
    assert summary.outcomes["merged"] == 1
    assert summary.outcomes["rejected"] == 4
    assert summary.outcomes["infra"] == 1
    assert summary.outcomes["error"] == 1

    # Rejection rate: rejected / (merged + rejected) = 4 / (1 + 4) = 0.8
    assert summary.rejection_rate == 0.8

    # Precision computed ONLY over gate_valid == True and oracle in {resolved, unresolved}
    # Here: inst-unres (unresolved) and inst-res (resolved) -> 1 / 2 = 0.5
    assert summary.scored_rejections == 2
    assert summary.resolved_rejections == 1
    assert summary.unresolved_rejections == 1
    assert summary.precision == 0.5
    assert summary.wilson_interval == (0.0945, 0.9055)

    # Exclusions: counted and named by instance id
    assert summary.exclusions["infra"] == ["inst-infra"]
    assert summary.exclusions["error"] == ["inst-error"]
    assert summary.exclusions["gate_invalid"] == ["inst-gv-false"]
    assert summary.exclusions["oracle_error"] == ["inst-oracle-err"]
    assert summary.exclusions["gate_validity_missing"] == []
    assert summary.exclusions["oracle_missing"] == []

    # Per-repo rejections: 2 django, 2 astropy
    assert summary.rejections_by_repo["django/django"] == 2
    assert summary.rejections_by_repo["astropy/astropy"] == 2

    # Scored count is 2 (< 10), so warning must be set
    assert summary.warning == SCREENING_WARNING


def test_unknown_outcome_raises_error(tmp_path: Path) -> None:
    """An unknown outcome value must raise a ValueError and not be skipped."""
    bad_line = {
        "instance_id": "test-bad",
        "repo": "django/django",
        "outcome": "unknown_outcome",
        "gate_valid": None,
        "oracle": None,
        "tokens": 100,
        "seconds": 10.0,
    }
    results_file = tmp_path / "bad_results.jsonl"
    results_file.write_text(json.dumps(bad_line) + "\n", encoding="utf-8")

    with pytest.raises(ValueError, match="unknown outcome"):
        load_results(results_file)


def test_zero_scored_rejections(tmp_path: Path) -> None:
    """With zero scored rejections, precision and interval are None (null/undefined)."""
    records = [
        {
            "instance_id": "m1",
            "repo": "repoA",
            "outcome": "merged",
            "gate_valid": None,
            "oracle": None,
            "tokens": 10,
            "seconds": 1.0,
        },
        {
            "instance_id": "rej-gv-false",
            "repo": "repoA",
            "outcome": "rejected",
            "gate_valid": False,
            "oracle": "unresolved",
            "tokens": 10,
            "seconds": 1.0,
        },
    ]
    results_file = tmp_path / "zero_scored.jsonl"
    with results_file.open("w", encoding="utf-8") as f:
        for r in records:
            f.write(json.dumps(r) + "\n")

    summary = summarize_results(load_results(results_file))
    assert summary.scored_rejections == 0
    assert summary.precision is None
    assert summary.wilson_interval is None
    assert summary.warning == SCREENING_WARNING

    d = summary.to_dict()
    assert d["precision"] is None
    assert d["wilson_interval"] is None


def test_screening_warning_threshold(tmp_path: Path) -> None:
    """Warning is emitted for <10 scored rejections and omitted for >=10."""

    def make_results(count: int) -> list[dict[str, object]]:
        return [
            {
                "instance_id": f"inst-{i}",
                "repo": "repoX",
                "outcome": "rejected",
                "gate_valid": True,
                "oracle": "unresolved",
                "tokens": 100,
                "seconds": 10.0,
            }
            for i in range(count)
        ]

    # 9 scored -> warning
    file9 = tmp_path / "nine.jsonl"
    file9.write_text(
        "\n".join(json.dumps(r) for r in make_results(9)) + "\n", encoding="utf-8"
    )
    summary9 = summarize_results(load_results(file9))
    assert summary9.scored_rejections == 9
    assert summary9.warning == SCREENING_WARNING

    # 10 scored -> no warning
    file10 = tmp_path / "ten.jsonl"
    file10.write_text(
        "\n".join(json.dumps(r) for r in make_results(10)) + "\n", encoding="utf-8"
    )
    summary10 = summarize_results(load_results(file10))
    assert summary10.scored_rejections == 10
    assert summary10.warning is None


def test_cli_human_and_json_output(tmp_path: Path) -> None:
    """CLI prints human summary by default, and one JSON object with --json."""
    records = [
        {
            "instance_id": "inst-1",
            "repo": "repoA",
            "outcome": "merged",
            "gate_valid": None,
            "oracle": None,
            "tokens": 10,
            "seconds": 1.0,
        },
        {
            "instance_id": "inst-2",
            "repo": "repoA",
            "outcome": "rejected",
            "gate_valid": True,
            "oracle": "unresolved",
            "tokens": 10,
            "seconds": 1.0,
        },
    ]
    file_path = tmp_path / "cli_test.jsonl"
    file_path.write_text(
        "\n".join(json.dumps(r) for r in records) + "\n", encoding="utf-8"
    )

    # Human output
    proc = subprocess.run(
        [sys.executable, str(SCRIPT), str(file_path)],
        capture_output=True,
        text=True,
        check=True,
    )
    human_out = proc.stdout
    assert "Instances: 2" in human_out
    assert "merged: 1" in human_out
    assert "rejected: 1" in human_out
    assert "Precision: 1.0000" in human_out
    assert SCREENING_WARNING in human_out

    # JSON output
    proc_json = subprocess.run(
        [sys.executable, str(SCRIPT), str(file_path), "--json"],
        capture_output=True,
        text=True,
        check=True,
    )
    assert proc_json.stdout.count("\n") == 1, "--json prints one line"
    data = json.loads(proc_json.stdout)
    assert set(data) == {
        "instances",
        "outcomes",
        "rejection_rate",
        "scored_rejections",
        "resolved",
        "unresolved",
        "precision",
        "wilson_interval",
        "exclusions",
        "rejections_by_repo",
        "warning",
    }, "one key per metric: no aliases in the --json contract"
    assert data["instances"] == 2
    assert data["outcomes"]["merged"] == 1
    assert data["outcomes"]["rejected"] == 1
    assert data["precision"] == 1.0
    assert data["wilson_interval"] == [0.2065, 1.0]
    assert data["warning"] == SCREENING_WARNING


def _write(tmp_path: Path, rows: list[dict[str, Any]]) -> Path:
    path = tmp_path / "results.jsonl"
    path.write_text("".join(json.dumps(r) + "\n" for r in rows), encoding="utf-8")
    return path


def _rejected(
    instance_id: str, gate_valid: bool | None, oracle: str | None
) -> dict[str, Any]:
    return {
        "instance_id": instance_id,
        "repo": "r",
        "outcome": "rejected",
        "gate_valid": gate_valid,
        "oracle": oracle,
        "tokens": 1,
        "seconds": 1.0,
    }


def test_missing_is_not_invalid_and_not_error(tmp_path: Path) -> None:
    """A validity check or verdict never recorded is its own exclusion, not a false or an error."""
    summary = summarize_results(
        load_results(
            _write(
                tmp_path,
                [
                    _rejected("no-gate-check", None, "unresolved"),
                    _rejected("no-verdict", True, None),
                    _rejected("scored", True, "unresolved"),
                ],
            )
        )
    )
    assert summary.exclusions["gate_validity_missing"] == ["no-gate-check"]
    assert summary.exclusions["oracle_missing"] == ["no-verdict"]
    assert summary.exclusions["gate_invalid"] == []
    assert summary.exclusions["oracle_error"] == []
    assert summary.scored_rejections == 1


def test_unknown_oracle_raises_error(tmp_path: Path) -> None:
    """An oracle value outside the contract is an error, not an exclusion."""
    with pytest.raises(ValueError, match="unknown oracle"):
        load_results(_write(tmp_path, [_rejected("x", True, "passed")]))
