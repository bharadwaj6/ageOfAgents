#!/usr/bin/env python3
"""Seeded sampler for gate_precision_run.sh.

Selects a reproducible random sample of SWE-bench instances that have a
non-empty PASS_TO_PASS list (required for --gate=repo).  The same
INSTANCES.json + --n + --seed always produces the same plan.json.

Usage:
    gate_precision_sample.py INSTANCES.json --n N --seed S [--out FILE]

Writes plan.json (or --out) containing the sampled instance ids in order.
"""
from __future__ import annotations

import argparse
import json
import random
import sys
from pathlib import Path


def load_instances(path: Path) -> list[dict]:
    """Load a JSON array or JSONL file of SWE-bench instances."""
    text = path.read_text().strip()
    if text.startswith("["):
        return json.loads(text)
    return [json.loads(line) for line in text.splitlines() if line.strip()]


def as_list(value: object) -> list:
    """Coerce a PASS_TO_PASS value (list or JSON string) to a Python list."""
    if isinstance(value, list):
        return value
    if isinstance(value, str):
        try:
            parsed = json.loads(value)
            return parsed if isinstance(parsed, list) else [value]
        except json.JSONDecodeError:
            return [value]
    return []


def eligible_ids(instances: list[dict]) -> list[str]:
    """Return sorted instance ids with a non-empty PASS_TO_PASS."""
    ids = [
        r["instance_id"]
        for r in instances
        if as_list(r.get("PASS_TO_PASS", []))
    ]
    return sorted(ids)


def sample(instance_path: Path, n: int, seed: int) -> list[str]:
    """Return n instance ids sampled deterministically from instance_path."""
    instances = load_instances(instance_path)
    pool = eligible_ids(instances)
    if n > len(pool):
        raise ValueError(
            f"requested {n} instances but only {len(pool)} have PASS_TO_PASS"
        )
    return random.Random(seed).sample(pool, n)


def main() -> None:
    ap = argparse.ArgumentParser(
        description=__doc__,
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )
    ap.add_argument("instances", type=Path, help="SWE-bench instances JSON/JSONL")
    ap.add_argument("--n", type=int, required=True, help="number of instances to sample")
    ap.add_argument("--seed", type=int, required=True, help="random seed")
    ap.add_argument(
        "--out",
        type=Path,
        default=Path("plan.json"),
        help="output file (default: plan.json)",
    )
    a = ap.parse_args()

    try:
        chosen = sample(a.instances, a.n, a.seed)
    except ValueError as e:
        print(f"error: {e}", file=sys.stderr)
        sys.exit(1)
    a.out.write_text(json.dumps(chosen, indent=2) + "\n")
    print(f"sampled {len(chosen)} instances → {a.out}", file=sys.stderr)


if __name__ == "__main__":
    main()
