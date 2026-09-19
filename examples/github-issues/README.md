# GitHub Issues front door

Label an issue `aoa` and it becomes an aoa Goal. aoa works it in an isolated worktree, merges only what
passes your Gate, and opens a pull request. The outcome comes back to the issue as one comment.

[`aoa-github.sh`](aoa-github.sh) is the reference front door from
[ADR 015](../../docs/design/adr/015-aoa-is-a-backend.md): front doors decide what gets worked on, and aoa
decides whether it lands. It is one bash script that uses only the [backend contract](../../docs/backend.md)
(`aoa goal`, `status`, `cancel` and `run`, with `--json`), `gh` and `jq`. It never reads `.aoa/`, which
shows the contract is enough to build a front door on. `make check` runs it against the real CLI and a
stand-in `gh` ([`cmd/aoa/frontdoor_test.go`](../../cmd/aoa/frontdoor_test.go)), so a change to the
contract that would break it fails the build.

## Prerequisites

- **`gh`, signed in as the account the comments come from** (`gh auth status`). The script asks
  `gh api user` who that is, so it needs a user token from `gh auth login` or a personal access token.
  The `GITHUB_TOKEN` of GitHub Actions cannot answer that call.
- **`jq`** (tested with 1.7 and 1.8) and **bash** 3.2 or later.
- **aoa with pull-request delivery**: the first release after 0.4.0, or `main` until then.
- **A workspace that adopts a clone of the repository and delivers pull requests** ([ADR
  016](../../docs/design/adr/016-deliver-a-goal-as-a-pull-request.md)). Create it with
  `aoa init --path ~/aoa/widgets --adopt ~/src/widgets`, then in its `aoa.toml` pick a real `backend`
  and change the `[delivery]` table to:

  ```toml
  [delivery]
    mode = "pr"   # push aoa/<goal-id> to origin, then open a pull request with gh
  ```

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `AOA_WS` | required | The aoa workspace to drive. |
| `AOA_GH_REPO` | required | The repository, as `owner/name`. |
| `AOA_GH_LABEL` | `aoa` | The label that hands an issue to aoa. |
| `AOA_GH_ALLOW` | the `gh` login | Logins trusted to write and label issues, separated by spaces or commas. |
| `AOA_BIN` | `aoa` | The aoa binary. |

## Use

```sh
export AOA_WS=~/aoa/widgets AOA_GH_REPO=acme/widgets AOA_GH_ALLOW="alice bob"
./aoa-github.sh cycle
```

| Command | What it does |
|---|---|
| `intake` | Submits each open issue with the label as a Goal, keyed `gh:owner/name#N/<attempt>`. Cancels the Goal of any issue that was closed or unlabelled. |
| `report` | Comments each Goal's outcome on its issue, once: the pull request, why it failed, that it was cancelled, or the `aoa approve` command for a change awaiting approval. |
| `cycle` | `intake`, then `aoa run`, then `report`. It reports even when `aoa run` exits `1` because a task failed. |

Each action logs one line to stderr. When a call about one issue fails, the script skips that issue,
carries on with the rest, and exits `1` at the end so a scheduler can alert. A configuration error, or a
failure to list the issues or read `aoa status`, stops it at once. Every step is idempotent, so the next
run picks up where this one stopped.

## Trust model

The issue text becomes instructions that an agent acts on, on the machine running aoa. Treat it as
untrusted input, bounded by the Gate, the sandbox and the budgets. Read
[`SECURITY.md`](../../SECURITY.md).

- **Both the author and the labeller must be trusted.** The script takes an issue only when both its
  author and the person who most recently applied the label are in `AOA_GH_ALLOW`. If someone outside
  the list removes the label and adds it again, the issue is skipped. Anyone with write access can still
  edit an issue after it is labelled, so put everyone with that access on the list, or keep aoa off
  repositories where that is too many people.
- **Its own comments are its only state.** Every comment ends in a hidden marker,
  `<!-- aoa:<goal>:<outcome> -->`. `report` posts only when that marker is not already in one of its own
  comments. `intake` counts its own final markers to choose the attempt number in the key. Markers in
  anyone else's comments are ignored, so nobody can fake an attempt or silence a report. Failure reasons
  are quoted with absolute paths removed, and cut to 300 characters.
- **A final outcome takes the label off before it comments.** To try again, a trusted person adds the
  label back, and the next attempt is a new Goal under a new key. If the comment fails after the label
  came off, the next `report` posts it.
- **It never approves.** With `require_approval`, it comments the `aoa approve` command and leaves the
  decision to whoever runs the workspace. See
  [What a front door must still do](../../docs/backend.md#what-a-front-door-must-still-do).

## Stopping work

- **Remove the label or close the issue.** The next `intake` cancels the Goal, and the next `report`
  says so on the issue. A cancelled Goal is never pushed. A merge that is already running when the cancel
  arrives can still finish on the Goal branch.
- **Close the pull request.** aoa stops once the pull request is open, so review and merging are yours.
- **Stop the schedule.** Nothing runs between cycles.

## Running on a schedule

`cycle` is meant to run from cron, a systemd timer or launchd, like `aoa run` in
[Scheduling](../../docs/scheduling.md). Scheduled jobs start with a minimal `PATH`, so set one that
finds `gh`, `jq` and `aoa`:

```cron
*/10 * * * * PATH=/usr/local/bin:/usr/bin:/bin AOA_WS=/srv/aoa/widgets AOA_GH_REPO=acme/widgets AOA_GH_ALLOW="alice bob" flock -n /tmp/aoa-github.lock /srv/aoa/aoa-github.sh cycle >>/var/log/aoa-github.log 2>&1
```

Don't let two cycles overlap. A second `aoa run` exits `75` and does no harm, but two `report`s racing
on one issue can both post a comment. `flock -n` skips a tick while the last one is still running. On
macOS, use launchd's `StartInterval`, which never starts a job that is still running.

## Limits

- `intake` reads at most 500 open issues with the label per run.
- `report` reads the comments of every issue whose Goal has an outcome, on every run. That is one API
  call per finished Goal, which suits hundreds of Goals within GitHub's 5,000 requests an hour. For many
  more, start a new workspace.
- On GitHub Enterprise, set `GH_HOST` for `gh`. Goals are matched to issues by the `owner/name/issues/N`
  path of their ref, so any host works.
