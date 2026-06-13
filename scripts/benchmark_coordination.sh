#!/usr/bin/env bash
set -euo pipefail

# Multi-Agent Coordination Benchmark for Age of Agents.
#
# Thin wrapper over the in-tree, hermetic benchmark harness (`aoa bench`,
# implemented in internal/bench). It runs the curated task suite under the
# single / planfirst / emergent strategies on the deterministic mock Backend and
# reports the docs/v2/metrics.md numbers computed by replaying the Event Log:
# coordination LLM sessions (0), merge correctness (100%), parallelism achieved,
# critical-path depth, and any invariant violations.
#
# Usage:
#   scripts/benchmark_coordination.sh            # markdown table
#   scripts/benchmark_coordination.sh --json     # machine-readable JSON

cd "$(dirname "$0")/.."

echo "Building aoa..."
go build -o aoa ./cmd/aoa

echo
echo "=========================================================="
echo "Coordination benchmark (hermetic, offline, mock Backend)"
echo "=========================================================="
./aoa bench "$@"
