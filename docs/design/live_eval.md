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

## Gate modes — what the number is measuring

`scripts/swebench_to_tasks.py --gate` decides what a proposal must pass to merge. It is independent of the
**oracle** (what decides "resolved"), which is always `FAIL_TO_PASS` run by the official Docker harness in
`eval_swebench_docker.sh`. Conflating the two is easy and makes numbers meaningless, so state the mode
next to any result you publish.

| `--gate` | Merge Gate | Use it for |
|---|---|---|
| `none` | `true` — every proposal merges | measuring the **harness** alone; this is what `--inference-mode` did |
| `f2p` | the issue's `FAIL_TO_PASS` tests | nothing. The agent iterates against its own grader; kept only to reproduce `eval_swebench.sh` |
| `repo` | the repo's own tests near the change | measuring **aoa** — a Gate that rejects broken patches without naming the answer |

Both arms must use the same instance set. The 5 astropy instances with an existing `--gate=none`
baseline (see below) are the cheapest starting point; regenerate the subset from the Lite split with:

```bash
python3 -c 'import json; rows={r["instance_id"]:r for r in json.load(open("scripts/swebench_lite.json"))}; \
ids=["astropy__astropy-12907","astropy__astropy-14182","astropy__astropy-14365","astropy__astropy-14995","astropy__astropy-6938"]; \
json.dump([rows[i] for i in ids], open("scripts/astropy_5.json","w"))'

GATE=none scripts/eval_swebench_docker.sh scripts/astropy_5.json grok 5 aoa-gateoff
GATE=repo scripts/eval_swebench_docker.sh scripts/astropy_5.json grok 5 aoa-gateon
```

`--gate=repo` skips any instance with no `PASS_TO_PASS` tests (it has nothing to gate on), so check the
task count matches across arms before comparing.

**Architecture note.** The per-instance images are x86_64-only. They *run* fine on Apple Silicon under
emulation, but *building* them locally does not work — the miniconda installer in the base image exits 255
under Rosetta — so `-n none` is not a workaround on arm64. Neither the harness nor aoa pulls images, so
each instance needs an explicit pull first:

```bash
docker pull --platform linux/amd64 swebench/sweb.eval.x86_64.astropy_1776_astropy-12907:latest
```

At ~3 GB per instance, pull → run both arms → `docker rmi` before the next instance keeps peak disk to one
image rather than the whole set.

The harness is pinned to `swebench==4.1.0`: 5.x removed `--cache_level` and the `[eval]` extra and expects
an `image` field the `princeton-nlp` dataset does not carry. Unpinned, phase 3 breaks after the agent has
already run, and results stop being comparable to the runs recorded below.

**Where the Gate runs.** A `repo` Gate needs the target repo's dependencies, which aoa does not provision
(ADR 009). The adapter therefore emits per-task `sandbox = "docker"` with `sandbox_image` set to the
instance's published SWE-bench image.

The Gate command copies the worktree into `/testbed` rather than being mounted there. This is not
incidental: the image keeps compiled extensions at `/testbed` (astropy ships 17 `.so` files) that the
agent's source tree does not contain, so mounting over that path hides them and every Gate fails on
import. `cp -a /workspace/. /testbed/` overlays the agent's sources and leaves the build products intact.
`--gate=none` emits no sandbox fields and runs on the host exactly as before.

`none` vs `repo` on the same instances, same backend, is the A/B that isolates what the verifier-gated
merge queue contributes. Only the delta is attributable to aoa: the absolute rate is dominated by the
backend harness, which is a swappable component (ADR 004).

## Prior runs (as of 2026-08-22)

Every run below was produced with **`--gate=none`** — `eval_swebench_docker.sh` hardcoded
`--inference-mode`, so `gate = [["true"]]` and every proposal merged unconditionally. Scoring was honest
(official SWE-bench Docker harness, oracle held out), but these numbers measure the backend's patch
quality with aoa orchestrating and **not gating**. The Gate's own contribution is unmeasured.

| Run | Backend | Resolved | Instances |
|---|---|---:|---|
| `aoa-20260614-044927` | claudecode | 1/1 | astropy-12907 |
| `aoa-20260614-144745` | claudecode | 3/5 | astropy-12907, -14182, -14365, -14995, -6938 |
| `aoa-20260615-222419` | grok | 10/11 | the 5 astropy + django-11001, -11019, -11039, -11049, -11283, -11422, -11564 |
| `aoa-debug-1781465753` | claudecode | 1/2 | astropy-12907, -14182 |
| `aoa-grok-smoke-3` | grok | 1/1 | astropy-12907 |
| `aoa-grok-smoke-5` | grok | 4/5 | astropy-12907, -14182, -14365, -14995, -6938 |

Two caveats on the 10/11:

- **Selection bias.** The instances are the head of the Lite split (astropy, then consecutive
  `django-110xx`), not a random sample. A rate on a hand-ordered prefix is not a Lite score.
- **Sample size.** At n=11 one instance is 9 points; at n=5 it is 20. These screen for regressions and
  for large effects. They cannot support a headline solve-rate.

On the shared 5-instance astropy set the two backends differ: grok 4/5, claudecode 3/5 — a reminder that
the harness, not the control plane, sets the level.
