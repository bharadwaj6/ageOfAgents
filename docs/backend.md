# Driving `aoa` from another system

`aoa` is built to be the backend of something else. A **front door** decides *what* gets worked on:
firstmate, a Symphony-style tracker poller, a Linear agent, a CI job, or a person. `aoa` decides
*whether it lands*: an isolated worktree, your Gate on the post-merge state, the merge queue,
approvals, budgets, and an Event Log that replays. The split is recorded in
[ADR 015](design/adr/015-aoa-is-a-backend.md).

This page is the contract a front door builds on. Every flag is in the [CLI reference](cli.md).

## The loop

```bash
# 1. Hand work over. The key makes a retry or a redelivery a no-op.
aoa goal --path "$WS" --json --source linear --ref "$ISSUE_URL" --key "linear:$ISSUE_ID" "$TEXT"
#    → {"schema":1,"goal_id":"g-1a2b3c4d","duplicate":false,"seq":7}

# 2. Make sure a Scheduler runs. Safe to call every time: a second run exits 75 and the
#    run already holding the workspace picks the goal up.
aoa run --path "$WS"            # or a cron / systemd timer / Actions schedule (see Scheduling)

# 3. Learn the outcome: poll a snapshot...
aoa status --path "$WS" --json  # goals[].outcome: queued | running | awaiting_approval | merged | delivered | failed | cancelled
#    ...or follow the log from where you left off.
aoa events --path "$WS" --json --since "$LAST_SEQ" --follow

# 4. Act on what needs a decision.
aoa approve --path "$WS" --json --by "$WHO" "$TICKET"
aoa cancel  --path "$WS" --json --by "$WHO" --reason "issue closed" "$GOAL"
```

## What you can rely on

| Guarantee | How |
|---|---|
| **Submitting is idempotent.** The same `--key` returns the same goal with `"duplicate":true` and writes nothing, even when several submitters race. | The check and the append happen atomically under the Event Log's cross-process lock. |
| **Any number of front doors can write at once.** | Appends are serialised across processes, so sequence numbers stay gapless. |
| **Exactly one Scheduler per workspace.** | `aoa run` holds an OS lock. A second run exits `75` without doing anything, and its goals are still picked up. |
| **Decisions are safe to retry.** Repeating an approve, reject or cancel returns `already_decided` / `already_cancelled` and writes nothing. | Checked and appended atomically, like submit. |
| **The log is a resumable stream.** `events --json --since N` prints every event after `N`, byte for byte. | Resume with the last `seq` you received. Partial lines are never emitted. |
| **Nothing lands that fails the Gate, or that was cancelled.** | The Gate runs on the post-merge state ([ADR 002](design/adr/002-verifier-gated-merge-queue.md)). A cancelled goal gets no new attempts, and its proposals are dropped before merging. A merge already executing when the cancel arrives can still complete. |
| **The JSON is versioned.** | Every result carries `schema`. Within a version, fields are only ever added. Ignore fields you don't know. |

## Delivery

By default a verified change lands on the branch the adopted repository has checked out, and nothing is
pushed. For a team repository set `[delivery] mode = "pr"`
([configuration](config-reference.md#delivery)), and each Goal becomes **one pull request**:

- The Goal's tasks merge onto its own branch, `aoa/<goal-id>`, cut from the remote base. The Gate
  guards that branch; the forge's required checks guard `main`.
- Once every task of the Goal is complete, aoa pushes the branch and opens the pull request. A failed or
  partial Goal is never pushed.
- `status --json` reports the Goal's `branch`, then `outcome: "delivered"` with its `pr_url`. While
  delivery is pending the Goal stays `running`. If a push or the opener fails, `delivery_error` says why
  and the next `aoa run` retries it. Until then `aoa run` exits `1`.
- aoa stops once the pull request is open. Review and merging belong to the forge and its people.

The events are `Delivered` and `DeliveryFailed`. `Merged` carries the Goal `branch` it landed on. The
design is [ADR 016](design/adr/016-deliver-a-goal-as-a-pull-request.md).

## Keys and refs

- **`--key` names one submission**, and it is global to the workspace. Prefix it with its source, such
  as `linear:ENG-123` or `gh:owner/repo#45`, so two trackers can't collide.
- **Retrying work that failed needs a new key.** The old key still names the failed goal.
- **Changing what an in-flight goal should do** is `aoa amend`, not a resubmit. Submitting the same key
  with new text is a duplicate: nothing changes and nothing is appended.
- **`--ref` is for you.** It is stored on the goal and returned by `status --json`, so a front door can
  map a goal back to its issue. `aoa serve` fills it with the GitHub issue URL.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | Success. For `run`, all work settled and nothing failed. |
| `1` | An error, or (for `run`) a task failed or a delivery is stuck. Tasks of a cancelled goal do not count. Read `status --json` for which one and why. |
| `2` | Usage: a flag could not be parsed. |
| `75` | `run` only: another Scheduler holds the workspace. Nothing to retry; your goal is on the log. |

## What a front door must still do

- **Decide who may approve.** `approve --by` records a name but cannot prove that a human made the call.
  If ADR 008's gate should mean *a person looked*, don't give `approve` to an automated front door.
- **Treat goal text as untrusted.** The agent runs what the model decides, on the machine running
  `aoa`. The Gate, the budgets and `require_approval` bound it. Read [`SECURITY.md`](https://github.com/bharadwaj6/ageOfAgents/blob/main/SECURITY.md).
- **Take the result to where people look.** With `[delivery] mode = "pr"` aoa opens the pull request
  itself (see [Delivery](#delivery)). Reporting back to the origin, such as a comment on the issue, is
  still yours ([#129](https://github.com/bharadwaj6/ageOfAgents/issues/129)). The
  [reference front door](#reference-front-door) does it with one issue comment per outcome.

## Reference front door

[`examples/github-issues`](https://github.com/bharadwaj6/ageOfAgents/tree/main/examples/github-issues)
is a complete front door in one bash script. It uses nothing but this contract, `gh` and `jq`. An issue
labelled `aoa` by a trusted person becomes a Goal and is delivered as a pull request, and its outcome goes
back to the issue as one comment. Removing the label or closing the issue cancels the Goal. The script
keeps no state of its own: markers in its own comments tell it what it has already reported. A
conformance test runs it against the real CLI in `make check`, so a change to the contract that would
break a front door fails the build.
