#!/usr/bin/env bash
# Smallest real-LLM proof that the live path works end-to-end: seed a repo with a
# deliberately failing test, hand aoa the goal, and let the claudecode backend fix
# it. Success = the Gate (go build + go test) passes and the change merges to main.
#
#   scripts/live_smoke.sh
#
# Requires: go and an authenticated `claude` CLI on PATH. No network dataset, no
# external services — this is the one-command "does aoa actually drive a real
# agent?" check.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "Building aoa..."
go build -o aoa ./cmd/aoa

WS="$(mktemp -d -t aoa-smoke.XXXXXX)"
echo "Workspace: $WS"
./aoa init --path "$WS" --repo ./repo >/dev/null

# Seed a real bug: Add returns 0 but the test expects 5. The Gate fails until fixed.
mkdir -p "$WS/repo/mathx"
cat > "$WS/repo/mathx/mathx.go" <<'GO'
// Package mathx provides small math helpers.
package mathx

// Add returns the sum of a and b.
func Add(a, b int) int {
	return 0 // BUG: should return a + b
}
GO
cat > "$WS/repo/mathx/mathx_test.go" <<'GO'
package mathx

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(2, 3); got != 5 {
		t.Fatalf("Add(2,3) = %d, want 5", got)
	}
}
GO
git -C "$WS/repo" add -A
git -C "$WS/repo" -c commit.gpgsign=false -c user.email=seed@aoa -c user.name=seed \
    commit -q -m "seed: failing mathx test"

sed -i.bak 's/backend = "mock"/backend = "claudecode"/' "$WS/aoa.toml"

./aoa goal --path "$WS" \
  "The mathx.Add function returns the wrong value; fix it so the package builds and all tests pass." >/dev/null

echo ""
echo "=== Running aoa with the live claudecode backend ==="
time ./aoa run --path "$WS"

echo ""
echo "=== MAST diagnosis ==="
./aoa diagnose --path "$WS"

echo ""
echo "=== Result on main ==="
git -C "$WS/repo" log --oneline -3
if (cd "$WS/repo" && go test ./... >/dev/null 2>&1); then
  echo "GATE GREEN: tests pass on main — the agent's fix merged."
else
  echo "GATE RED: tests still fail — inspect $WS"
  exit 1
fi
