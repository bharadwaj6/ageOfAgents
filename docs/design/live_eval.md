# Live evaluation — running aoa with a real agent

The hermetic suite (mock backend) proves the coordination machinery is correct; this page is about the
other half — *does aoa actually drive a real LLM to a green, merged change?* The harness for it is
`internal/liveeval` + `aoa eval` (ADR 009); the scripts below make it one command.

## First live run (the smoke test)

`scripts/live_smoke.sh` seeds a repo with a deliberately failing test (`mathx.Add` returns `0`; a test
expects `5`), hands aoa the goal, and lets the `claudecode` backend fix it. Observed end-to-end:

```
#1 GoalSubmitted   #2 TicketCreated   #3 TicketReady   #4 TicketClaimed
#5 WorkStarted     #6 ProposalSubmitted   #7 VerificationPassed   #8 Merged

mathx.Add now returns `a + b`; `go build ./... && go test ./...` is green on main.
aoa diagnose: No MAST failure modes detected (clean run).
```

This run also surfaced a real backend bug: headless `claude -p` runs but **declines to edit files**
without a permission mode, so every Task failed with *"agent produced no changes."* Fixed by defaulting
the `claudecode` backend to `--permission-mode acceptEdits` (`internal/agent/claudecode.go`); the worktree
is the agent's sandbox and the Gate, not the agent, decides what merges. Regression-guarded by
`TestNewClaudeCodeAllowsEdits`.

Run it yourself (needs `go` and an authenticated `claude` CLI):

```bash
make smoke          # or, directly:
scripts/live_smoke.sh
```

## SWE-bench Lite

`scripts/eval_swebench.sh` turns a SWE-bench (Lite) instances file into `aoa eval` tasks and runs them:

```bash
# INSTANCES.json: the SWE-bench_Lite test split exported to JSON (or JSONL).
scripts/eval_swebench.sh INSTANCES.json claudecode 5     # first 5 instances
```

What it does (`scripts/swebench_to_tasks.py`):
- clones each instance's `repo` at its `base_commit`, and **normalizes it to a `main` branch** — aoa cuts
  every worker branch from `main` and keeps `main` always-green (`worktree.go`), so the adapter points
  `main` at the base commit; no aoa change needed;
- writes a `tasks.toml` where the Goal is the issue's `problem_statement` and both the merge **Gate** and
  the **success oracle** are the issue's `FAIL_TO_PASS` tests. The agent must make those tests pass for its
  change to merge — and the same tests decide success.

Each run reports task success, tokens, the MAST histogram, and invariant violations (must be 0).

### Environment caveat (important)

aoa owns orchestration and verification; it does **not** provision Python environments. The adapter does
not `pip install` each repo's dependencies, so `python -m pytest` will fail on import unless you run inside
a prepared environment for the target repos — e.g. the official SWE-bench Docker images, or a venv with the
repo installed. This is the deliberate ADR 009 boundary: the caller prepares the repo + env; aoa runs the
agent and the Gate. The HuggingFace dataset host is fetched by you (it is firewalled off from CI/sandboxes).

## Reading the numbers

- **success** — the success oracle passed on the final `main` *and* no invariant was violated.
- **MAST** — failure-mode count from `aoa diagnose`; the live counterpart to the hermetic 0.
- **violations** — any breach of the merge invariants (I1/I2/I4, approval gate). Should stay 0 even live:
  the Gate + merge queue keep `main` green regardless of how the agent behaves.

The mock numbers prove correctness by construction; these live numbers measure efficacy. We never fold one
into the other (ADR 009).
