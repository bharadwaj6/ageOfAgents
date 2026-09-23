#!/usr/bin/env bash
# Zero-cost stand-in coding agent for Gate-precision smoke runs.
#
# Part of #103 / #211. The per-instance runner
# (scripts/gate_precision_run.sh) has a rejection path (Gate-validity
# re-run, prediction export, SWE-bench harness) that only a real Gate
# rejection exercises. This fixture writes a root conftest.py that
# raises on import so every pytest collection errors, the Gate rejects,
# and the SWE-bench oracle marks the instance unresolved — all without
# spending a token.
#
# Register it as a type = "cli" backend:
#
#   [backends.badpatch]
#   type = "cli"
#   bin  = "scripts/gate_precision_badpatch.sh"
#
# Only fits pytest-based instances: a root conftest.py is imported by
# pytest before collection. Non-pytest Gates will not see this change
# as a failure.
#
# aoa invokes this in the task worktree with the prompt as the last
# argv element. The prompt is ignored.
set -euo pipefail

if [[ -e conftest.py ]]; then
  echo "gate_precision_badpatch: refuse to overwrite existing conftest.py" >&2
  exit 1
fi

cat > conftest.py <<'PY'
raise RuntimeError("deliberate bad patch from gate_precision_badpatch fixture")
PY

echo "wrote conftest.py (deliberate bad patch)"
exit 0
