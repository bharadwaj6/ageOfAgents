#!/usr/bin/env python3
"""Summarize Gate precision with a Wilson score interval.

Part of #103 (Gate precision). Turn a Gate-precision run's per-instance
results into an honest precision rate and 95% Wilson score interval.

Input schema contract (results.jsonl, one JSON object per line):
  {"instance_id": "...", "repo": "...", "outcome": "...",
   "gate_valid": true|false|null, "oracle": "...",
   "tokens": 123456, "seconds": 412.5}
"""

import argparse
import json
import math
import sys
from collections.abc import Iterable
from dataclasses import dataclass
from pathlib import Path
from typing import Any

VALID_OUTCOMES: set[str] = {"merged", "rejected", "infra", "error"}
WILSON_Z_95: float = 1.959963984540054
MIN_SCORED_FOR_RATE: int = 10
SCREENING_WARNING: str = "screening only — too few rejections to support a rate"


@dataclass(frozen=True)
class InstanceResult:
    """A single instance's outcome and scoring record."""

    instance_id: str
    repo: str
    outcome: str
    gate_valid: bool | None
    oracle: str | None
    tokens: int
    seconds: float


@dataclass(frozen=True)
class PrecisionSummary:
    """Aggregated Gate precision metrics and exclusions."""

    total_instances: int
    outcomes: dict[str, int]
    rejection_rate: float | None
    scored_rejections: int
    resolved_rejections: int
    unresolved_rejections: int
    precision: float | None
    wilson_interval: tuple[float, float] | None
    exclusions: dict[str, list[str]]
    rejections_by_repo: dict[str, int]
    warning: str | None

    def to_dict(self) -> dict[str, Any]:
        """Convert summary metrics to a serializable dictionary."""
        infra_ids = self.exclusions.get("infra", [])
        error_ids = self.exclusions.get("error", [])
        gv_false_ids = self.exclusions.get("gate_valid == false", [])
        oracle_err_ids = self.exclusions.get('oracle == "error"', [])

        exclusions_map = {
            "infra": infra_ids,
            "error": error_ids,
            "gate_valid == false": gv_false_ids,
            "gate_valid_false": gv_false_ids,
            'oracle == "error"': oracle_err_ids,
            "oracle_error": oracle_err_ids,
        }
        exclusion_counts = {
            "infra": len(infra_ids),
            "error": len(error_ids),
            "gate_valid == false": len(gv_false_ids),
            "gate_valid_false": len(gv_false_ids),
            'oracle == "error"': len(oracle_err_ids),
            "oracle_error": len(oracle_err_ids),
        }

        return {
            "instances": self.total_instances,
            "total_instances": self.total_instances,
            "merged": self.outcomes.get("merged", 0),
            "rejected": self.outcomes.get("rejected", 0),
            "infra": self.outcomes.get("infra", 0),
            "error": self.outcomes.get("error", 0),
            "outcomes": dict(self.outcomes),
            "rejection_rate": self.rejection_rate,
            "scored_rejections": self.scored_rejections,
            "resolved": self.resolved_rejections,
            "unresolved": self.unresolved_rejections,
            "precision": self.precision,
            "wilson_interval": (
                list(self.wilson_interval) if self.wilson_interval else None
            ),
            "wilson_lower": (
                self.wilson_interval[0] if self.wilson_interval else None
            ),
            "wilson_upper": (
                self.wilson_interval[1] if self.wilson_interval else None
            ),
            "exclusions": exclusions_map,
            "exclusion_counts": exclusion_counts,
            "rejections_by_repo": dict(self.rejections_by_repo),
            "rejections_per_repo": dict(self.rejections_by_repo),
            "warning": self.warning,
        }


def wilson_score_interval(
    k: int, n: int, z: float = WILSON_Z_95
) -> tuple[float, float] | None:
    """Compute a Wilson score confidence interval rounded to 4 decimal places.

    Returns None when n == 0. Raises ValueError if k is not in [0, n].
    """
    if n == 0:
        return None
    if k < 0 or k > n:
        raise ValueError(f"k must be in [0, {n}], got {k}")

    p = k / n
    z2 = z * z
    denom = 1.0 + (z2 / n)
    center = (p + (z2 / (2.0 * n))) / denom
    spread = (z * math.sqrt((p * (1.0 - p) / n) + (z2 / (4.0 * n * n)))) / denom

    lower = max(0.0, center - spread)
    upper = min(1.0, center + spread)
    return round(lower, 4), round(upper, 4)


def parse_results(
    lines: Iterable[str], source_name: str = "<input>"
) -> list[InstanceResult]:
    """Parse JSONL lines into InstanceResult objects, validating outcome values."""
    results: list[InstanceResult] = []
    for line_no, raw_line in enumerate(lines, start=1):
        line = raw_line.strip()
        if not line:
            continue
        try:
            row = json.loads(line)
        except json.JSONDecodeError as exc:
            raise ValueError(f"{source_name}:{line_no}: invalid JSON: {exc}") from exc

        if not isinstance(row, dict):
            raise TypeError(
                f"{source_name}:{line_no}: expected JSON object, got {type(row).__name__}"
            )

        outcome = row.get("outcome")
        if outcome not in VALID_OUTCOMES:
            raise ValueError(
                f"{source_name}:{line_no}: unknown outcome {outcome!r} "
                f"for instance {row.get('instance_id')!r}"
            )

        results.append(
            InstanceResult(
                instance_id=str(row["instance_id"]),
                repo=str(row["repo"]),
                outcome=str(outcome),
                gate_valid=row.get("gate_valid"),
                oracle=row.get("oracle"),
                tokens=int(row.get("tokens", 0)),
                seconds=float(row.get("seconds", 0.0)),
            )
        )
    return results


def load_results(path: Path) -> list[InstanceResult]:
    """Load and parse instances from a results.jsonl file."""
    with path.open("r", encoding="utf-8") as f:
        return parse_results(f, source_name=str(path))


def summarize_results(results: list[InstanceResult]) -> PrecisionSummary:
    """Compute precision, rejection rate, exclusions, and Wilson interval."""
    total_instances = len(results)
    outcomes: dict[str, int] = {
        "merged": 0,
        "rejected": 0,
        "infra": 0,
        "error": 0,
    }

    infra_ids: list[str] = []
    error_ids: list[str] = []
    gv_false_ids: list[str] = []
    oracle_error_ids: list[str] = []

    resolved_count = 0
    unresolved_count = 0

    rejections_by_repo: dict[str, int] = {r.repo: 0 for r in results}

    for r in results:
        outcomes[r.outcome] = outcomes.get(r.outcome, 0) + 1

        if r.outcome == "infra":
            infra_ids.append(r.instance_id)
        elif r.outcome == "error":
            error_ids.append(r.instance_id)
        elif r.outcome == "merged":
            pass
        elif r.outcome == "rejected":
            rejections_by_repo[r.repo] += 1
            if r.gate_valid is False or r.gate_valid is None:
                gv_false_ids.append(r.instance_id)
            elif r.oracle == "error":
                oracle_error_ids.append(r.instance_id)
            elif r.oracle == "resolved":
                resolved_count += 1
            elif r.oracle == "unresolved":
                unresolved_count += 1
            else:
                # Any other unexpected or missing oracle on a valid rejection
                oracle_error_ids.append(r.instance_id)

    merged = outcomes["merged"]
    rejected = outcomes["rejected"]
    total_decided = merged + rejected
    rejection_rate = round(rejected / total_decided, 4) if total_decided > 0 else None

    scored_rejections = resolved_count + unresolved_count
    if scored_rejections > 0:
        precision = round(unresolved_count / scored_rejections, 4)
        interval = wilson_score_interval(unresolved_count, scored_rejections)
    else:
        precision = None
        interval = None

    warning = (
        SCREENING_WARNING if scored_rejections < MIN_SCORED_FOR_RATE else None
    )

    exclusions = {
        "infra": infra_ids,
        "error": error_ids,
        "gate_valid == false": gv_false_ids,
        'oracle == "error"': oracle_error_ids,
    }

    return PrecisionSummary(
        total_instances=total_instances,
        outcomes=outcomes,
        rejection_rate=rejection_rate,
        scored_rejections=scored_rejections,
        resolved_rejections=resolved_count,
        unresolved_rejections=unresolved_count,
        precision=precision,
        wilson_interval=interval,
        exclusions=exclusions,
        rejections_by_repo=rejections_by_repo,
        warning=warning,
    )


def format_human(summary: PrecisionSummary) -> str:
    """Format the PrecisionSummary into human-readable text."""
    lines: list[str] = [
        f"Instances: {summary.total_instances}",
        "Outcomes:",
        f"  merged: {summary.outcomes.get('merged', 0)}",
        f"  rejected: {summary.outcomes.get('rejected', 0)}",
        f"  infra: {summary.outcomes.get('infra', 0)}",
        f"  error: {summary.outcomes.get('error', 0)}",
    ]

    if summary.rejection_rate is not None:
        merged = summary.outcomes.get("merged", 0)
        rejected = summary.outcomes.get("rejected", 0)
        lines.append(
            f"Rejection rate: {summary.rejection_rate:.4f} ({rejected}/{merged + rejected})"
        )
    else:
        lines.append("Rejection rate: undefined")

    lines.append("")
    if summary.precision is not None and summary.wilson_interval is not None:
        lines.append(
            f"Precision: {summary.precision:.4f} "
            f"({summary.unresolved_rejections}/{summary.scored_rejections} scored rejections)"
        )
        lines.append(
            f"Wilson 95% interval: "
            f"[{summary.wilson_interval[0]:.4f}, {summary.wilson_interval[1]:.4f}]"
        )
    else:
        lines.append("Precision: undefined")
        lines.append("Wilson 95% interval: undefined")

    lines.append("")
    lines.append("Exclusions:")
    categories = [
        ("infra", "infra"),
        ("error", "error"),
        ("gate_valid == false", "gate_valid == false"),
        ('oracle == "error"', 'oracle == "error"'),
    ]
    for label, key in categories:
        items = summary.exclusions.get(key, [])
        if items:
            lines.append(f"  {label}: {len(items)} ({', '.join(items)})")
        else:
            lines.append(f"  {label}: 0")

    lines.append("")
    lines.append("Rejections by repo:")
    if summary.rejections_by_repo:
        for repo in sorted(summary.rejections_by_repo.keys()):
            lines.append(f"  {repo}: {summary.rejections_by_repo[repo]}")
    else:
        lines.append("  (none)")

    if summary.warning:
        lines.append("")
        lines.append(f"warning: {summary.warning}")

    return "\n".join(lines)


def main() -> None:
    """Run the CLI entry point."""
    parser = argparse.ArgumentParser(
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    parser.add_argument("results", type=Path, help="Path to results.jsonl")
    parser.add_argument(
        "--json",
        action="store_true",
        dest="json_output",
        help="Print summary as a single JSON object",
    )
    args = parser.parse_args()

    if not args.results.exists():
        print(f"error: file not found: {args.results}", file=sys.stderr)
        sys.exit(1)

    try:
        results = load_results(args.results)
        summary = summarize_results(results)
    except (ValueError, TypeError, OSError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        sys.exit(1)

    if args.json_output:
        print(json.dumps(summary.to_dict(), indent=2))
    else:
        print(format_human(summary))


if __name__ == "__main__":
    main()
