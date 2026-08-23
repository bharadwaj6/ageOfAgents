# scripts/

Helper scripts for benchmarking and evaluation. **None of these are needed to use `aoa`** — the binary is
self-contained. They exist to measure it.

| Script | What it does | Needs |
|---|---|---|
| `live_smoke.sh` | One real end-to-end run against a real backend — the fastest check that a backend works at all | an authenticated agent CLI |
| `benchmark_coordination.sh` | Wraps `aoa bench`, the hermetic coordination benchmark | nothing (offline) |
| `benchmark_live.sh` | The same shape against a real backend | an authenticated agent CLI |
| `eval_swebench_docker.sh` | The SWE-bench harness: builds tasks, runs `aoa eval`, scores with the **official** SWE-bench Docker harness | Docker, the SWE-bench images, `uv` |
| `eval_swebench.sh` | Older SWE-bench runner. **Its Gate is the oracle** — the agent iterates against the same tests it is scored on. Kept for comparison; do not cite its numbers | Docker |
| `swebench_to_tasks.py` | Converts SWE-bench instances into `aoa eval` task files. `--gate=none\|f2p\|repo` selects what the merge Gate checks, independently of the oracle | Python 3 |
| `extract_swebench_patches.py` | Pulls the model patch out of a run's Event Log into a predictions file | Python 3 |
| `gate_precision.py` | Turns Gate-rejected proposals into a predictions file, so the oracle can say how many rejections were justified. Filters out sandbox faults | Python 3 |
| `otel_smoke.sh` | Sends a run's replay to an OTLP endpoint end to end | an OTLP collector, `.env` |

## The distinction that matters

**Gate ≠ oracle.** The Gate is `aoa`'s merge criterion — what a proposal must pass to land. The oracle is
SWE-bench's held-out `FAIL_TO_PASS`/`PASS_TO_PASS`, scored afterwards by the official harness. Letting the
Gate see the oracle inflates results and measures nothing; `eval_swebench.sh` does exactly that, which is
why its numbers are not quoted anywhere.

See [`../docs/design/live_eval.md`](../docs/design/live_eval.md) for the protocol and what has actually
been run.
