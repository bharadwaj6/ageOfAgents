#!/usr/bin/env bash
set -e

# Multi-Agent Coordination Benchmark for Age of Agents
# This script injects a predefined Diamond Dependency task graph to test
# parallel execution, dependency gating, and merge queue serialization.

BACKEND="${1:-mock}" # default to mock since claudecode is rate-limited

echo "Building aoa..."
go build -o aoa ./cmd/aoa

WORK_DIR=$(mktemp -d -t aoa_multi_benchmark.XXXXXX)
echo "Created temporary workspace at $WORK_DIR"

echo "Initializing workspace..."
./aoa init --path "$WORK_DIR" --repo ./multi_repo

# Modify aoa.toml to use the specified backend and ensure concurrency is 4
CONFIG_FILE="$WORK_DIR/aoa.toml"
sed -i.bak "s/backend = \".*\"/backend = \"$BACKEND\"/" "$CONFIG_FILE"
sed -i.bak "s/concurrency = .*/concurrency = 4/" "$CONFIG_FILE"

echo "Injecting Diamond Dependency Task Graph into Ledger..."

# Use a small Python script to inject the JSONL events
cat << 'EOF' > "$WORK_DIR/inject.py"
import json
import sys
from datetime import datetime, timezone

ws_path = sys.argv[1]
led_path = f"{ws_path}/.aoa/events.jsonl"

goal_id = "g-multi-01"
ts = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")

events = [
    {
        "seq": 1, "type": "GoalSubmitted", "ts": ts, "actor": "human",
        "payload": {"goal_id": goal_id, "text": "Build a chat app with backend, frontend, and integration tests."}
    },
    {
        "seq": 2, "type": "TicketCreated", "ts": ts, "actor": "orchestrator",
        "payload": {"ticket_id": goal_id + "-t1", "goal_id": goal_id, "title": "Task 1: Define Shared API Types", "idempotency_key": goal_id + "-t1"}
    },
    {
        "seq": 3, "type": "TicketCreated", "ts": ts, "actor": "orchestrator",
        "payload": {"ticket_id": goal_id + "-t2", "goal_id": goal_id, "title": "Task 2: Implement Backend Server", "idempotency_key": goal_id + "-t2", "depends_on": [goal_id + "-t1"]}
    },
    {
        "seq": 4, "type": "TicketCreated", "ts": ts, "actor": "orchestrator",
        "payload": {"ticket_id": goal_id + "-t3", "goal_id": goal_id, "title": "Task 3: Implement Frontend UI", "idempotency_key": goal_id + "-t3", "depends_on": [goal_id + "-t1"]}
    },
    {
        "seq": 5, "type": "TicketCreated", "ts": ts, "actor": "orchestrator",
        "payload": {"ticket_id": goal_id + "-t4", "goal_id": goal_id, "title": "Task 4: Write E2E Integration Test", "idempotency_key": goal_id + "-t4", "depends_on": [goal_id + "-t2", goal_id + "-t3"]}
    }
]

with open(led_path, "a") as f:
    for e in events:
        f.write(json.dumps(e) + "\n")

print("Successfully injected task graph into ledger.")
EOF

# Run the injector
python3 "$WORK_DIR/inject.py" "$WORK_DIR"

echo ""
echo "=========================================================="
echo "Starting Orchestrator..."
echo "=========================================================="
time ./aoa run --path "$WORK_DIR"

echo ""
echo "=========================================================="
echo "Run Complete! Final Status:"
echo "=========================================================="
./aoa status --path "$WORK_DIR"

echo ""
echo "=========================================================="
echo "Git Log (main branch in multi_repo):"
echo "=========================================================="
cd "$WORK_DIR/multi_repo"
git log --oneline --graph
