#!/usr/bin/env python3
"""Summarize Gate misses: merged proposals that break existing tests.

Part of #103 (Gate precision). The precision runner scores what the Gate
rejected. This scores what it merged. A Gate miss is a merged arm whose
held-out PASS_TO_PASS tests fail.

Input schema (merged_scores.jsonl, one JSON object per line):
  {"instance_id": "...", "repo": "...", "backend": "...",
   "resolved": true|false|null,
   "f2p_failed": ["..."], "p2p_failed": ["..."],
   "error": null|"..."}

``resolved``, ``f2p_failed`` and ``p2p_failed`` come from the official
harness report (``tests_status.FAIL_TO_PASS.failure`` and
``tests_status.PASS_TO_PASS.failure``). A line with ``error`` set was not
scored. Use ``--backend NAME`` to summarise one arm; without it the tool
exits non-zero when the file mixes backends.
"""

from __future__ import annotations

import argparse
import json
import sys
from collections.abc import Iterable
from dataclasses import dataclass
from pathlib import Path
from typing import Any

try:
    from precision_summary import (
        MIN_SCORED_FOR_RATE,
        SCREENING_WARNING,
        wilson_score_interval,
    )
except ModuleNotFoundError:
    from scripts.precision_summary import (
        MIN_SCORED_FOR_RATE,
        SCREENING_WARNING,
        wilson_score_interval,
    )


@dataclass(frozen=True)
class HarnessVerdict:
    """One instance's resolved flag and failing test ids."""

    resolved: bool | None
    f2p_failed: tuple[str, ...]
    p2p_failed: tuple[str, ...]
    error: str | None

    def to_dict(self) -> dict[str, Any]:
        """Serialize the verdict for the score file."""
        return {
            "resolved": self.resolved,
            "f2p_failed": list(self.f2p_failed),
            "p2p_failed": list(self.p2p_failed),
            "error": self.error,
        }


@dataclass(frozen=True)
class MergedScore:
    """One scored (instance, backend) arm."""

    instance_id: str
    repo: str
    backend: str | None
    resolved: bool | None
    f2p_failed: tuple[str, ...]
    p2p_failed: tuple[str, ...]
    error: str | None


@dataclass(frozen=True)
class MissSummary:
    """Gate-miss counts for one backend arm."""

    backend: str | None
    scored: int
    errors: int
    error_instances: tuple[tuple[str, str], ...]
    gate_misses: int
    miss_rate: float | None
    wilson_interval: tuple[float, float] | None
    resolved: int
    resolve_rate: float | None
    misses: tuple[tuple[str, tuple[str, ...]], ...]
    warning: str | None

    def to_dict(self) -> dict[str, Any]:
        """Convert the summary to one JSON object."""
        return {
            "backend": self.backend,
            "scored": self.scored,
            "errors": self.errors,
            "error_instances": [
                {"instance_id": iid, "error": reason}
                for iid, reason in self.error_instances
            ],
            "gate_misses": self.gate_misses,
            "miss_rate": self.miss_rate,
            "wilson_interval": list(self.wilson_interval)
            if self.wilson_interval
            else None,
            "resolved": self.resolved,
            "resolve_rate": self.resolve_rate,
            "misses": [
                {"instance_id": iid, "p2p_failed": list(tests)}
                for iid, tests in self.misses
            ],
            "warning": self.warning,
        }


def _failure_ids(tests: dict[str, Any], key: str) -> tuple[str, ...]:
    """Return ``tests[key].failure`` as test ids.

    A missing block means the harness recorded no failures for that class.
    """
    block = tests.get(key) or {}
    if not isinstance(block, dict):
        raise TypeError(f"{key} is not an object")
    failed = block.get("failure") or []
    if not isinstance(failed, list) or any(not isinstance(item, str) for item in failed):
        raise ValueError(f"{key} failure is not a list of test ids")
    return tuple(failed)


def _verdict_error(reason: str) -> HarnessVerdict:
    """A report that is not a verdict."""
    return HarnessVerdict(None, (), (), reason)


def parse_harness_report(report: dict[str, Any], instance_id: str) -> HarnessVerdict:
    """Read resolved, FAIL_TO_PASS failures and PASS_TO_PASS failures.

    The harness writes ``report.json`` as ``{instance_id: {resolved,
    tests_status}}``. ``tests_status`` is omitted when the patch never ran;
    that is still a verdict, with no failing ids. A report that has no
    boolean ``resolved`` for this instance is an error.
    """
    if not isinstance(report, dict):
        raise TypeError("report must be a JSON object")
    inst = report.get(instance_id)
    if not isinstance(inst, dict):
        return _verdict_error("report missing instance")
    resolved = inst.get("resolved")
    if not isinstance(resolved, bool):
        return _verdict_error("report missing resolved")
    tests = inst.get("tests_status")
    if tests is None:
        return HarnessVerdict(resolved, (), (), None)
    if not isinstance(tests, dict):
        return _verdict_error("report tests_status is not an object")
    try:
        f2p = _failure_ids(tests, "FAIL_TO_PASS")
        p2p = _failure_ids(tests, "PASS_TO_PASS")
    except (TypeError, ValueError) as exc:
        return _verdict_error(str(exc))
    return HarnessVerdict(resolved, f2p, p2p, None)


def _test_ids(value: Any, field: str, source: str) -> tuple[str, ...]:
    """Require a JSON list of test-id strings."""
    if not isinstance(value, list) or any(not isinstance(item, str) for item in value):
        raise ValueError(f"{source}: {field} must be a list of test ids")
    return tuple(value)


def parse_scores(lines: Iterable[str], source_name: str = "<input>") -> list[MergedScore]:
    """Parse merged_scores.jsonl into MergedScore rows."""
    scores: list[MergedScore] = []
    for line_no, raw_line in enumerate(lines, start=1):
        line = raw_line.strip()
        if not line:
            continue
        where = f"{source_name}:{line_no}"
        try:
            row = json.loads(line)
        except json.JSONDecodeError as exc:
            raise ValueError(f"{where}: invalid JSON: {exc}") from exc
        if not isinstance(row, dict):
            raise TypeError(f"{where}: expected JSON object, got {type(row).__name__}")
        resolved = row.get("resolved")
        if resolved is not None and not isinstance(resolved, bool):
            raise ValueError(f"{where}: resolved must be true, false or null")
        error = row.get("error")
        if error is not None and not isinstance(error, str):
            raise ValueError(f"{where}: error must be a string or null")
        backend_raw = row.get("backend")
        backend = str(backend_raw) if backend_raw is not None else None
        scores.append(
            MergedScore(
                instance_id=str(row["instance_id"]),
                repo=str(row["repo"]),
                backend=backend,
                resolved=resolved,
                f2p_failed=_test_ids(row.get("f2p_failed", []), "f2p_failed", where),
                p2p_failed=_test_ids(row.get("p2p_failed", []), "p2p_failed", where),
                error=error,
            )
        )
    return scores


def load_scores(path: Path) -> list[MergedScore]:
    """Load merged scores from a JSONL file."""
    with path.open("r", encoding="utf-8") as handle:
        return parse_scores(handle, source_name=str(path))


def select_backend(
    scores: list[MergedScore], backend: str | None
) -> tuple[list[MergedScore], str | None]:
    """Return (rows, effective backend).

    When *backend* is given, keep only that arm. A name that matches nothing
    is an error. When *backend* is omitted and the file holds more than one
    arm, exit non-zero rather than pool them. Rows with no backend are an arm
    of their own.
    """
    if backend is not None:
        filtered = [row for row in scores if row.backend == backend]
        if not filtered:
            print(f"error: no rows for backend {backend!r}", file=sys.stderr)
            sys.exit(1)
        return filtered, backend

    backends: set[str | None] = {row.backend for row in scores}
    if len(backends) > 1:
        names = ", ".join(sorted(b if b is not None else "(no backend)" for b in backends))
        print(
            f"error: results file contains more than one backend ({names}); "
            "use --backend NAME to summarise a single arm",
            file=sys.stderr,
        )
        sys.exit(1)
    return scores, next(iter(backends), None)


def summarize_scores(scores: list[MergedScore], backend: str | None = None) -> MissSummary:
    """Count Gate misses, the resolve rate, and a Wilson 95% interval.

    A line is scored when it has a boolean ``resolved`` and no ``error``.
    A Gate miss is a scored line with at least one ``p2p_failed`` id. The
    miss rate and the resolve rate use the scored lines as the denominator.
    """
    error_instances: list[tuple[str, str]] = []
    misses: list[tuple[str, tuple[str, ...]]] = []
    scored = 0
    resolved_count = 0
    for row in scores:
        if row.error:
            error_instances.append((row.instance_id, row.error))
            continue
        if row.resolved is None:
            error_instances.append((row.instance_id, "no verdict"))
            continue
        scored += 1
        if row.resolved:
            resolved_count += 1
        if row.p2p_failed:
            misses.append((row.instance_id, row.p2p_failed))

    miss_count = len(misses)
    if scored > 0:
        miss_rate = round(miss_count / scored, 4)
        resolve_rate = round(resolved_count / scored, 4)
        interval = wilson_score_interval(miss_count, scored)
    else:
        miss_rate = None
        resolve_rate = None
        interval = None
    warning = SCREENING_WARNING if scored < MIN_SCORED_FOR_RATE else None
    return MissSummary(
        backend=backend,
        scored=scored,
        errors=len(error_instances),
        error_instances=tuple(error_instances),
        gate_misses=miss_count,
        miss_rate=miss_rate,
        wilson_interval=interval,
        resolved=resolved_count,
        resolve_rate=resolve_rate,
        misses=tuple(misses),
        warning=warning,
    )


def format_human(summary: MissSummary) -> str:
    """Format the summary as plain text."""
    lines: list[str] = []
    if summary.backend is not None:
        lines.append(f"Backend: {summary.backend}")
    lines.append(f"Scored: {summary.scored}")
    lines.append(f"Errors: {summary.errors}")
    for instance_id, reason in summary.error_instances:
        lines.append(f"  {instance_id}: {reason}")
    lines.append("")
    if summary.miss_rate is not None and summary.wilson_interval is not None:
        lines.append(
            f"Gate misses: {summary.gate_misses}/{summary.scored} "
            f"({summary.miss_rate:.4f})"
        )
        lower, upper = summary.wilson_interval
        lines.append(f"Wilson 95% interval: [{lower:.4f}, {upper:.4f}]")
    else:
        lines.append("Gate misses: 0")
        lines.append("Wilson 95% interval: undefined")
    if summary.resolve_rate is not None:
        lines.append(
            f"Resolve rate: {summary.resolve_rate:.4f} "
            f"({summary.resolved}/{summary.scored})"
        )
    else:
        lines.append("Resolve rate: undefined")
    lines.append("")
    lines.append("Misses:")
    if summary.misses:
        for instance_id, tests in summary.misses:
            lines.append(f"  {instance_id}: {', '.join(tests)}")
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
    parser.add_argument("scores", type=Path, help="Path to merged_scores.jsonl")
    parser.add_argument(
        "--json",
        action="store_true",
        dest="json_output",
        help="Print the summary as a single JSON object",
    )
    parser.add_argument(
        "--backend",
        metavar="NAME",
        default=None,
        help="Summarise only rows for this backend",
    )
    args = parser.parse_args()

    if not args.scores.exists():
        print(f"error: file not found: {args.scores}", file=sys.stderr)
        sys.exit(1)

    try:
        scores = load_scores(args.scores)
        filtered, effective = select_backend(scores, args.backend)
        summary = summarize_scores(filtered, backend=effective)
    except (ValueError, TypeError, OSError, KeyError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        sys.exit(1)

    if args.json_output:
        print(json.dumps(summary.to_dict()))
    else:
        print(format_human(summary))


if __name__ == "__main__":
    main()
