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
the `claudecode` backend to `--permission-mode acceptEdits` (now a preset row in
`internal/agent/cli.go`; ADR 014); the worktree is the agent's sandbox and the Gate, not the agent,
decides what merges. Regression-guarded by `TestCLIPresetArgv`, which asserts the flag for every
harness that needs one.

## Two runs on a real Go repository (2026-08-23, `grok`)

Beyond the seeded smoke test: two runs against an existing Go repo, each asked for a specific missing
unit test.

| Gate | Result | Attempts | Wall | Tokens |
|---|---|---|---|---|
| `go build` + one package's tests | merged, correct | 1 | 105s | — *(usage was unreported then)* |
| `go build` + `go vet` + **the full suite** | merged, correct | **2** | 344s | 606,669 |

The second is the interesting one. Its first attempt burned 316k tokens and was **abandoned before it
produced a proposal** — the Gate never saw it — and the retry passed the full suite and merged, leaving
`main` green. So the retry path works. What the run also showed is that an abandoned attempt is
expensive and, until it was fixed, recorded no reason anywhere. Re-running either settled workspace does
no work and exits `0`.

**Two tasks are not a solve-rate.** They are evidence that the loop closes end to end, and no more.

A third run on 2026-08-24 with the `codex` backend merged a correct change in one `aoa run`
(590,623 tokens, 2 attempts, 141.7s) — and exposed a real bug while doing it: `max_passes` was an
accidental wall-clock timeout that killed any run longer than ~100 seconds of agent time. The hermetic
suite could not have found it, because the `mock` backend returns in milliseconds.

## Run the smoke test yourself

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
| `repo` | the repo's own tests in the files the issue touches | measuring **aoa** — a Gate that rejects broken patches without naming the answer |

`repo` gates on whole test *files*, derived from the `PASS_TO_PASS` ids, not on the ids themselves. Those
ids are recorded against the post-test-patch tree, so an id for a parametrised case the held-out test
patch adds does not exist at `base_commit`: pytest exits "not found" and the Gate fails on every instance
regardless of the agent's work. The files exist either way, and at `base_commit` they contain exactly the
repo's pre-existing tests. `FAIL_TO_PASS` ids are passed as `--deselect`, so a reproduce test that already
exists is never part of the Gate; pytest ignores a `--deselect` matching nothing, which is the usual case.

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

## Gate A/B (2026-08-23, grok backend, at HEAD)

The first runs where `--gate=none` and `--gate=repo` are compared on the same instance with the same
backend, both scored by the official Docker harness on held-out `FAIL_TO_PASS`.

| Instance | Gate | Merged | Attempts | Rejected | Duration | Violations | Resolved |
|---|---|---|---:|---:|---:|---|:---:|
| astropy-12907 | none | 1 | 1 | 0% | 208s | 0 | ✅ |
| astropy-12907 | repo | 1 | 1 | 0% | 132s | 0 | ✅ |
| astropy-14365 | none | 1 | 1 | 0% | 236s | 0 | ✅ |
| astropy-14365 | repo | 1 | 2 | **50%** | 306s | 0 | ✅ |

**What this shows.** On 14365 the Gate did exactly what it exists to do: it rejected a proposal that broke
the repo's own existing tests, the agent retried against the Gate's output, and the second proposal passed
both the Gate and the held-out oracle. The mechanism works end to end against a real backend, and its cost
is visible — one extra attempt and ~70s on that instance.

**What this does not show.** Both configurations resolved both instances, so the Gate has not yet been
shown to change an *outcome*.

## Gate precision

The counterfactual the A/B cannot supply: would a rejected proposal have failed the oracle? A Gate that
rejects nothing is useless, and one that rejects good work is worse than no Gate, so the number that
matters is **precision** — the fraction of rejections the oracle would also have rejected.

Measuring it needs the rejected patch, which a retry normally discards. Run with `--max-attempts 1`: every
rejection becomes terminal, the orchestrator preserves the worktree (the existing warm-handoff path), and
`aoa eval --json` reports the recovered diff under `rejected_patches`. `scripts/gate_precision.py` turns
those into a predictions file the official harness scores like any other:

```bash
GATE=repo scripts/eval_swebench_docker.sh scripts/astropy_5.json grok 5   # --max-attempts 1
scripts/gate_precision.py aoa_report.json rejected_predictions.json
uv run --with "swebench==4.1.0" python -m swebench.harness.run_evaluation \
    --predictions_path rejected_predictions.json --run_id gate-precision \
    --dataset_name princeton-nlp/SWE-bench_Lite --split test --cache_level env
```

**Per-instance resumable runner.** At ~3 GB per image, pulling the whole set up front is impractical.
`scripts/gate_precision_run.sh INSTANCES.json BACKEND` measures precision one instance at a time: pull →
eval → oracle → `docker rmi`, keeping peak disk to one image. It records outcomes in `RUN_DIR/results.jsonl`
(one JSON line per instance) and skips ids already present, so a stopped run resumes from where it left off.
Before every `aoa eval` on that instance — each arm, and the Gate-validity `mock` run — the task
repository is reset to the commit recorded in `instances/<id>/base_sha` at prepare time
(`scripts/gate_precision_reset.sh`), so one arm's merge cannot become the next arm's base. A resumed
instance that has `tasks.toml` but no `base_sha` is recorded as `error`.
The seeded sampler (`scripts/gate_precision_sample.py --n N --seed S`) selects a reproducible subset of
instances that have a non-empty `PASS_TO_PASS`; `STOP_AFTER_REJECTIONS` (default 30) bounds the run.
Preview the plan without touching docker with `DRY_RUN=1`, and summarise a run with
`scripts/precision_summary.py RUN_DIR/results.jsonl`. `aoa eval` makes its worktrees under `RUN_DIR/tmp`, so
a [confined harness](../harnesses/agy.md) needs its root to contain `RUN_DIR`.

**Two-arm comparison.** Set `BACKENDS="agy grok"` to run two pinned backends against the same sampled
instances: the image is pulled once per instance, each backend is evaluated in turn, and `results.jsonl`
records one line per `(instance, backend)` pair with a `backend` field.  Each arm is stopped independently
once it reaches `STOP_AFTER_REJECTIONS`; the other continues until its own limit.  Summarise a single arm
with `scripts/precision_summary.py RUN_DIR/results.jsonl --backend agy`; omitting `--backend` when the
file contains more than one backend is an error, preventing inadvertent pooling of the two arms.

**Exclude sandbox faults first.** A gate that could not run is not a verdict on the patch. The first
precision sweep (2026-08-23, 4 instances) produced 2 rejections and **both were spurious**: replaying each
rejected patch through the same gate at `base_commit` passed (13 and 179 tests), so neither said anything
about the agent's work. One was byte-identical to a patch that had already resolved.

That exposed a real defect, now fixed: `verify.Run` treated *any* non-zero exit as a failed gate, so a
docker outage, a missing image or an OOM was recorded as "the patch is broken". The gate still blocks the
merge in that case — failing closed is right — but the failure is now flagged `Infra`, the reason reads
`gate could not run (sandbox failure)`, and `gate_precision.py` drops those before computing precision.
`TicketFailed` also carries the gate output now; without it a terminal failure recorded only the command
that failed, which is why diagnosing this needed a manual reproduction.

**Every remaining rejected patch the harness marks resolved is a Gate false positive** — work the Gate discarded
that would have fixed the issue. Precision = 1 − (resolved rejections / total rejections).

**Sample size.** Two instances, four runs. This screens for mechanism and regressions; it supports no
rate. Note also that 14365 failed in *every* June run under both backends and now passes ungated on the
first attempt — with no Gate and no retries, aoa's control plane cannot be the cause, so the `grok` CLI
itself improved. The June baselines are therefore no longer a like-for-like comparison.

**A false result worth recording.** The first `--gate=repo` attempt reported 0/1 with the Gate rejecting
both attempts — which reads exactly like "the Gate caught a bad patch". It was a broken Gate: it ran
`PASS_TO_PASS` node ids, which are recorded against the post-test-patch tree, so ids for parametrised
cases the held-out test patch adds exited "not found" and the Gate failed on every instance regardless of
the agent's work. Gating on test files fixed it. A Gate that can never pass produces a plausible number,
so check any new Gate against `base_commit` before believing a rejection.

### Protocol for #103 (pre-registered 2026-09-23, before any run)

Fixed before the first instance runs, so the result cannot be shaped after the fact. A change after the
pilot is recorded here with its date and reason. Nothing changes once the main run starts.

**Question.** Of the proposals the `repo` Gate rejects, what fraction would the held-out SWE-bench oracle
also reject? That is precision, reported with a Wilson 95% interval. It is not a solve rate. The rejection
rate is reported beside it, because precision over a handful of rejections says little.

**Two arms, each pinned.** Precision is conditional on the backend, since the rejections come from its
mistakes. So there are two arms, each one backend and one model, run on the same instances and reported
separately, never pooled:

| Arm | Backend | Model |
|---|---|---|
| A | `agy` (Antigravity CLI 1.2.8) | `gemini-3.8-flash-high` |
| B | `grok` (grok CLI 1.0.41) | `grok-4.7`, `--reasoning-effort high` |

Both run confined (see the [agy](../harnesses/agy.md) and [grok](../harnesses/grok.md) pages), with
identical worker instructions. A router that picks the model per request is not eligible as an arm,
because what it measured could not be named. A weaker model makes more rejections, so it reaches a usable
sample sooner, but its broken patches are also easier to reject, which tends to flatter precision. The
protocol states this rather than corrects for it. If the two arms disagree, that is itself a finding.

**Instances.** A seeded random sample (`SEED=103`) of the SWE-bench Lite instances with a non-empty
`PASS_TO_PASS`, which is 294 of the 300. Not the head of the split: that was the selection bias in the
earlier runs. The repo mix is reported.

**Run.** `--gate=repo` and `--max-attempts 1`, so every rejection is terminal and its patch is kept. One
instance at a time with `scripts/gate_precision_run.sh`: the image is pulled once, used by both arms,
then removed.

**What counts.** A rejection is scored only if all three hold:

- the Gate ran. A sandbox failure is recorded as `infra` and not scored;
- the Gate is valid for that instance: the same Gate passes a null patch (the `mock` backend) at
  `base_commit`, recorded as `gate_valid`. This is the check that would have caught the broken Gate
  above. It is a property of the instance, so it runs once and both arms share it;
- the official harness (`swebench==4.1.0`) returned a verdict.

Every exclusion is counted and named. A scored rejection that the oracle marks *resolved* is a Gate false
positive.

**Stopping rule.** Each arm stops at 30 scored rejections. The run ends when both arms have stopped, or
after 100 instances. A 10-instance pilot comes first, to measure the rejection rate and the time per
instance. If the pilot shows fewer than 2 rejections in an arm, the main run is re-sized before it starts,
and that decision is recorded here.

**What gets published, per arm:**

- precision with its interval;
- the rejection rate;
- the count of every exclusion;
- the backend, model, seed and harness version;
- the per-instance `results.jsonl`, with local paths removed.

Below 10 scored rejections, an arm is reported as a screen, not a rate. A null result, where the Gate
rarely rejects anything, is published as a finding, not dropped.

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
