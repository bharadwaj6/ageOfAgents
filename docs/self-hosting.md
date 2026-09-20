# Pointing aoa at your own repository

This is the setup the maintainer runs against `aoa` itself: an issue labelled `aoa` becomes a Goal, an
agent does the work in a throwaway worktree, the Gate runs, and a pull request appears for review. It is
the [backend contract](backend.md), [PR delivery](design/adr/016-deliver-a-goal-as-a-pull-request.md)
and the [GitHub Issues front door](https://github.com/bharadwaj6/ageOfAgents/tree/main/examples/github-issues)
put together.

**Run it yourself, on a budget.** Nothing here installs a scheduler. A cycle is a command you start; if
you later decide to schedule one, decide it deliberately and give it a budget you would be content to
lose every day ([ADR 017](design/adr/017-spend-is-bounded-before-it-happens.md)).

## 1. A dedicated clone, never your working copy

In PR mode the repository is left checked out at a Goal branch, so give aoa its own clone:

```bash
mkdir -p ~/aoa-dogfood/{bin,logs}
gh repo clone you/your-repo ~/aoa-dogfood/repo
```

Add a `pre-push` hook to that clone. The agent's worktrees share it, so it binds the agent too:

```sh
#!/bin/sh
# ~/aoa-dogfood/repo/.git/hooks/pre-push — only aoa/<goal> branches may leave this clone.
while read -r _ _ remote_ref _; do
  case "$remote_ref" in refs/heads/aoa/*) ;; *) echo "refusing $remote_ref" >&2; exit 1 ;; esac
done
```

Run your Gate there twice by hand before trusting it. If it fails for a reason of its own, every Goal
will fail.

## 2. Pin the binary and the script

```bash
GOBIN=~/aoa-dogfood/bin go install github.com/bharadwaj6/ageOfAgents/cmd/aoa@v0.5.0
cp examples/github-issues/aoa-github.sh ~/aoa-dogfood/bin/
```

Pinning outside the clone means a change aoa proposes only affects aoa after you merge it and reinstall.

## 3. The workspace

```bash
~/aoa-dogfood/bin/aoa init --path ~/aoa-dogfood/ws --adopt ~/aoa-dogfood/repo
```

Then in `~/aoa-dogfood/ws/aoa.toml`:

```toml
backend      = "claudecode"
concurrency  = 1                      # a tight budget wants one attempt at a time
max_attempts = 2
agent_timeout = "45m"
verify = [["make", "check"]]          # your Gate; add your linter if CI runs one
max_usd_per_goal = 3.0

[delivery]
mode = "pr"                           # remote "origin", base "main", opener `gh`

[budget]
usd_per_day        = 10.0
goals_per_day      = 3
require_run_budget = true             # `aoa run` refuses without --max-usd

[backends.claudecode]                 # your own settings and hooks stay out of unattended runs,
type = "cli"                          # and the harness caps what one attempt may spend
bin  = "claude"
args = ["--permission-mode", "acceptEdits", "--output-format", "json", "--setting-sources", "project",
        "--max-budget-usd", "2", "--allowedTools", "Bash(go build:*)", "Bash(go test:*)", "-p"]
```

Check it with `aoa doctor --path ~/aoa-dogfood/ws`.

## 4. Run a cycle

```bash
export AOA_WS=~/aoa-dogfood/ws AOA_GH_REPO=you/your-repo AOA_GH_ALLOW=you
AOA_RUN_MAX_USD=3 ~/aoa-dogfood/bin/aoa-github.sh cycle
```

Label one issue `aoa` and watch the first one through. GitHub's issue search lags a few seconds behind a
label, so a cycle started immediately may see nothing; run it again.

What you should see: a Goal submitted, an agent attempt, your Gate, a PR on `aoa/<goal>`, a comment on
the issue with the PR link and what it cost, and the label removed. Retrying takes a human re-labelling.

## 5. Knowing what it did, and stopping it

| | |
|---|---|
| What happened | `aoa status --path $AOA_WS` — the day's spend against its budget, and every Goal's outcome |
| Detail | `aoa events --path $AOA_WS --json --since N`, `aoa diagnose` |
| Stop one issue | Remove its label; the next cycle cancels the Goal |
| Stop everything | Don't start a cycle. If you scheduled one, unload the schedule. |
| Take over a failure | The "needs human" line names the worktree it preserved |

## What this costs

From the maintainer's own runs, each a single attempt on a subscription:

| Issue | Tokens | Cost as the harness reported it |
|---|---:|---:|
| A one-paragraph docs change | 125k | not yet metered |
| A small Go fix with a test | 557k | $0.31 |
| Another, in a different package | 446k | $0.25 |

Tokens include cache reads, which is why the numbers look large next to the dollars. One goal that
needed two attempts used 2.7M tokens. Set `max_usd_per_goal` and a per-attempt cap accordingly.

## Before you point it at something that matters

- **The agent is not sandboxed.** It runs as you, with your credentials.
  Read [`SECURITY.md`](https://github.com/bharadwaj6/ageOfAgents/blob/main/SECURITY.md).
- **Only trusted people should label.** `AOA_GH_ALLOW` gates both the author and the labeller.
- **Your Gate is the guarantee.** aoa merges nothing your build and tests do not pass, so a weak Gate
  means weak PRs.
- **The forge owns `main`.** aoa opens a pull request; your required checks and your review decide.
