#!/usr/bin/env bash
set -e

# Live Benchmark Script for Age of Agents
# This script tests the framework E2E using the real "claudecode" backend.
# It creates a temporary workspace, submits a standardized goal, and runs the orchestrator.

echo "Building aoa..."
go build -o aoa ./cmd/aoa

WORK_DIR=$(mktemp -d -t aoa_benchmark.XXXXXX)
echo "Created temporary workspace at $WORK_DIR"

# Ensure we clean up on exit (optional, maybe keep it for debugging)
# trap 'rm -rf "$WORK_DIR"' EXIT
echo "Note: Workspace is kept at $WORK_DIR for debugging."

echo "Initializing workspace..."
./aoa init --path "$WORK_DIR" --repo ./benchmark_repo

# Modify aoa.toml to use claudecode
echo "Configuring aoa.toml to use claudecode backend..."
CONFIG_FILE="$WORK_DIR/aoa.toml"
sed -i.bak 's/backend = "mock"/backend = "claudecode"/' "$CONFIG_FILE"

# The standard benchmark task
GOAL="Implement a generic, thread-safe LRU cache in an 'lru' package with Get, Put, and Remove methods, and 100% test coverage."

echo "Submitting goal: $GOAL"
./aoa goal --path "$WORK_DIR" "$GOAL"

echo ""
echo "=========================================================="
echo "Starting Orchestrator..."
echo "=========================================================="
# Measure time taken
time ./aoa run --path "$WORK_DIR"

echo ""
echo "=========================================================="
echo "Run Complete! Final Status:"
echo "=========================================================="
./aoa status --path "$WORK_DIR"

echo ""
echo "=========================================================="
echo "Event Feed Summary:"
echo "=========================================================="
./aoa feed --path "$WORK_DIR"

echo ""
echo "=========================================================="
echo "Git Log (main branch in benchmark_repo):"
echo "=========================================================="
cd "$WORK_DIR/benchmark_repo"
git log --oneline
