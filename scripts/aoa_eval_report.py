#!/usr/bin/env python3
"""Read one task's result out of an `aoa eval --json` report.

gate_precision_run.sh reads the report through this, so its shape is read in
one tested place.

Usage:
    aoa_eval_report.py outcome REPORT TASK   # merged | rejected | infra | error
    aoa_eval_report.py tokens  REPORT TASK   # tokens spent, 0 if absent
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


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("field", choices=("outcome", "tokens"))
    ap.add_argument("report", type=Path)
    ap.add_argument("task")
    a = ap.parse_args()
    rep = find(json.loads(a.report.read_text()), a.task)
    print(outcome(rep) if a.field == "outcome" else tokens(rep))


if __name__ == "__main__":
    main()
