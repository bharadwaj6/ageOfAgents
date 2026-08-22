#!/usr/bin/env python3
"""Score the proposals aoa's Gate rejected, to measure Gate precision.

A Gate that rejects nothing is useless; a Gate that rejects good work is worse
than none. Precision is the fraction of rejections the success oracle would also
have rejected — i.e. how often the Gate was right to block a merge.

Input is an `aoa eval --json` report produced with `max_attempts = 1` (so every
rejection is terminal and its patch is preserved; see swebench_to_tasks.py
--max-attempts). This turns each rejected patch into a SWE-bench prediction so
the official Docker harness can score it exactly like a merged one:

    aoa eval --tasks tasks.toml --backend grok --json > report.json
    scripts/gate_precision.py report.json rejected_predictions.json
    python -m swebench.harness.run_evaluation \\
        --predictions_path rejected_predictions.json --run_id gate-precision ...

Then: every rejected patch the harness marks RESOLVED is a Gate false positive —
work the Gate threw away that would in fact have fixed the issue.
"""
import argparse
import json
import sys


def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("report", help="aoa eval --json output")
    ap.add_argument("out", help="predictions.json to write")
    ap.add_argument("--model", default="aoa-rejected",
                    help="model_name_or_path recorded in the predictions")
    a = ap.parse_args()

    reports = json.load(open(a.report))
    preds, tasks_with_rejects = [], 0
    for rep in reports:
        rejected = rep.get("rejected_patches") or []
        if not rejected:
            continue
        tasks_with_rejects += 1
        for i, rp in enumerate(rejected):
            # The instance id is the task name; a suffix keeps multiple rejected
            # proposals for one instance distinguishable in the harness output.
            preds.append({
                "instance_id": rep["task"],
                "model_name_or_path": a.model if i == 0 else f"{a.model}-{i}",
                "model_patch": rp["diff"],
            })
            print(f"  {rep['task']}: rejected — {rp['reason'][:90]}", file=sys.stderr)

    json.dump(preds, open(a.out, "w"), indent=2)
    print(f"wrote {len(preds)} rejected prediction(s) from {tasks_with_rejects} task(s) "
          f"to {a.out}", file=sys.stderr)
    if not preds:
        print("no rejected patches — either the Gate rejected nothing, or the run "
              "did not use max_attempts = 1 (retried rejections leave no worktree)",
              file=sys.stderr)


if __name__ == "__main__":
    main()
