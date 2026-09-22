"""Tests for gate_precision_sample.py."""
from __future__ import annotations

import json
from pathlib import Path

import gate_precision_sample as gps
import pytest

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------


def make_instances(tmp_path: Path, rows: list[dict]) -> Path:
    """Write rows as a JSON array to a temp file and return the path."""
    p = tmp_path / "instances.json"
    p.write_text(json.dumps(rows))
    return p


# ---------------------------------------------------------------------------
# as_list
# ---------------------------------------------------------------------------


def test_as_list_already_list() -> None:
    assert gps.as_list(["a", "b"]) == ["a", "b"]


def test_as_list_json_string() -> None:
    assert gps.as_list('["a", "b"]') == ["a", "b"]


def test_as_list_plain_string() -> None:
    assert gps.as_list("test::foo") == ["test::foo"]


def test_as_list_none() -> None:
    assert gps.as_list(None) == []


def test_as_list_empty_list() -> None:
    assert gps.as_list([]) == []


# ---------------------------------------------------------------------------
# load_instances
# ---------------------------------------------------------------------------


def test_load_instances_json_array(tmp_path: Path) -> None:
    rows = [{"instance_id": "a"}, {"instance_id": "b"}]
    p = make_instances(tmp_path, rows)
    loaded = gps.load_instances(p)
    assert [r["instance_id"] for r in loaded] == ["a", "b"]


def test_load_instances_jsonl(tmp_path: Path) -> None:
    p = tmp_path / "instances.jsonl"
    p.write_text('{"instance_id": "a"}\n{"instance_id": "b"}\n')
    loaded = gps.load_instances(p)
    assert [r["instance_id"] for r in loaded] == ["a", "b"]


# ---------------------------------------------------------------------------
# eligible_ids
# ---------------------------------------------------------------------------


def make_rows() -> list[dict]:
    return [
        {"instance_id": "repo__proj-1", "PASS_TO_PASS": ["test_a::foo"]},
        {"instance_id": "repo__proj-2", "PASS_TO_PASS": []},           # empty list → excluded
        {"instance_id": "repo__proj-3", "PASS_TO_PASS": "[]"},          # empty JSON string → excluded
        {"instance_id": "repo__proj-4", "PASS_TO_PASS": '["test_b"]'},  # non-empty JSON string → included
        {"instance_id": "repo__proj-5"},                                # missing key → excluded
    ]


def test_eligible_ids_filters_empty() -> None:
    ids = gps.eligible_ids(make_rows())
    assert "repo__proj-2" not in ids
    assert "repo__proj-3" not in ids
    assert "repo__proj-5" not in ids


def test_eligible_ids_includes_nonempty() -> None:
    ids = gps.eligible_ids(make_rows())
    assert "repo__proj-1" in ids
    assert "repo__proj-4" in ids


def test_eligible_ids_sorted() -> None:
    ids = gps.eligible_ids(make_rows())
    assert ids == sorted(ids)


# ---------------------------------------------------------------------------
# sample — determinism
# ---------------------------------------------------------------------------


def test_sample_deterministic(tmp_path: Path) -> None:
    """Same inputs always produce the same plan."""
    rows = [
        {"instance_id": f"repo__proj-{i}", "PASS_TO_PASS": ["t"]}
        for i in range(20)
    ]
    p = make_instances(tmp_path, rows)
    first = gps.sample(p, n=5, seed=42)
    second = gps.sample(p, n=5, seed=42)
    assert first == second


def test_sample_seed_changes_result(tmp_path: Path) -> None:
    """Different seeds produce different plans (with high probability)."""
    rows = [
        {"instance_id": f"repo__proj-{i}", "PASS_TO_PASS": ["t"]}
        for i in range(20)
    ]
    p = make_instances(tmp_path, rows)
    a = gps.sample(p, n=5, seed=1)
    b = gps.sample(p, n=5, seed=2)
    assert a != b


def test_sample_correct_count(tmp_path: Path) -> None:
    rows = [
        {"instance_id": f"repo__proj-{i}", "PASS_TO_PASS": ["t"]}
        for i in range(10)
    ]
    p = make_instances(tmp_path, rows)
    chosen = gps.sample(p, n=3, seed=99)
    assert len(chosen) == 3


def test_sample_excludes_no_pass_to_pass(tmp_path: Path) -> None:
    rows = [
        {"instance_id": "repo__proj-good", "PASS_TO_PASS": ["t"]},
        {"instance_id": "repo__proj-bad", "PASS_TO_PASS": []},
    ]
    p = make_instances(tmp_path, rows)
    chosen = gps.sample(p, n=1, seed=0)
    assert "repo__proj-bad" not in chosen


def test_sample_raises_when_too_few(tmp_path: Path) -> None:
    rows = [{"instance_id": "repo__proj-1", "PASS_TO_PASS": ["t"]}]
    p = make_instances(tmp_path, rows)
    with pytest.raises(ValueError, match="only 1 have PASS_TO_PASS"):
        gps.sample(p, n=5, seed=0)


# ---------------------------------------------------------------------------
# CLI (plan.json output)
# ---------------------------------------------------------------------------


def test_cli_writes_plan(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    """Running main() writes plan.json with the chosen ids."""
    rows = [
        {"instance_id": f"repo__proj-{i}", "PASS_TO_PASS": ["t"]}
        for i in range(10)
    ]
    instances_path = make_instances(tmp_path, rows)
    out_path = tmp_path / "plan.json"

    monkeypatch.setattr(
        "sys.argv",
        [
            "gate_precision_sample.py",
            str(instances_path),
            "--n", "3",
            "--seed", "7",
            "--out", str(out_path),
        ],
    )
    gps.main()

    chosen = json.loads(out_path.read_text())
    assert len(chosen) == 3
    # Reproducible: running again gives the same list.
    gps.main()
    assert json.loads(out_path.read_text()) == chosen
