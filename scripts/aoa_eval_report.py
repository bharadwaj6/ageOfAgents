#!/usr/bin/env python3
"""Read one task's result out of an `aoa eval --json` report.

gate_precision_run.sh reads the report through this, so its shape is read in
one tested place.

Usage:
    aoa_eval_report.py outcome REPORT TASK   # merged | rejected | infra | error
    aoa_eval_report.py tokens  REPORT TASK   # tokens spent, 0 if absent
    aoa_eval_report.py rejections RESULTS BACKEND  # gate-valid rejections so far
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path

SANDBOX = "sandbox failure"


def find(reports: list[dict], task: str) -> dict | None:
    """Return the report for task, or None."""
    return next((rep for rep in reports if rep.get("task") == task), None)


def outcome(rep: dict | None) -> str:
    """Classify a task: merged, rejected by the Gate, Gate could not run, or error."""
    if rep is None:
        return "error"
    # Counts live under "metrics", not at the top level.
    if (rep.get("metrics") or {}).get("merged"):
        return "merged"
    rejected = rep.get("rejected_patches") or []
    if any(SANDBOX not in (p.get("reason") or "") for p in rejected):
        return "rejected"
    if rejected:
        return "infra"
    return "error"


def tokens(rep: dict | None) -> int:
    """Tokens the task spent."""
    return int(((rep or {}).get("metrics") or {}).get("tokens_total") or 0)


def count_gate_valid_rejections_per_backend(results_path: Path) -> dict[str, int]:
    """Return a mapping of backend name -> gate-valid rejection count.

    Reads a results.jsonl written by gate_precision_run.sh.  Lines that have no
    ``backend`` field are counted under the empty string ``""``.  Blank lines are
    skipped; a corrupt line raises, because skipping it would miscount the stop rule.
    """
    counts: dict[str, int] = {}
    if not results_path.exists():
        return counts
    for raw in results_path.read_text(encoding="utf-8").splitlines():
        raw = raw.strip()
        if not raw:
            continue
        row = json.loads(raw)
        if not isinstance(row, dict):
            continue
        backend = str(row.get("backend", ""))
        if row.get("outcome") == "rejected" and row.get("gate_valid") is True:
            counts[backend] = counts.get(backend, 0) + 1
    return counts


def main() -> None:
    """CLI entry point."""
    ap = argparse.ArgumentParser(
        description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter
    )
    ap.add_argument("field", choices=("outcome", "tokens", "rejections"))
    ap.add_argument("report", type=Path, help="aoa eval --json report, or results.jsonl for rejections")
    ap.add_argument("task", help="task id, or the backend name for rejections")
    a = ap.parse_args()
    if a.field == "rejections":
        print(count_gate_valid_rejections_per_backend(a.report).get(a.task, 0))
        return
    rep = find(json.loads(a.report.read_text()), a.task)
    print(outcome(rep) if a.field == "outcome" else tokens(rep))


if __name__ == "__main__":
    main()
