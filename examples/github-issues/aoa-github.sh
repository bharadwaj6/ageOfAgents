#!/usr/bin/env bash
# aoa-github.sh: a reference GitHub Issues front door for aoa (ADR 015).
#
# Labelled issues become aoa Goals, and each Goal's outcome goes back to its
# issue as one comment. It uses nothing but aoa's CLI JSON contract
# (docs/backend.md) and the GitHub CLI, and keeps no state of its own: aoa's
# Event Log holds the Goals, and the comments it wrote are its cursor.
# README.md next to this file explains the trust model.
#
# Every value that comes from GitHub or aoa is read with jq into a quoted
# variable and passed as one argument. None of it is spliced into a command
# string or a jq filter.
set -euo pipefail

usage() {
  cat <<'EOF'
usage: aoa-github.sh intake|report|cycle

  intake  submit each open issue carrying the label whose author and labeller are
          trusted, and cancel the goals of issues since closed or unlabelled
  report  comment each goal's outcome on its issue, exactly once
  cycle   intake, then aoa run, then report

environment:
  AOA_WS        aoa workspace to drive (required)
  AOA_GH_REPO   GitHub repository as owner/name (required)
  AOA_GH_LABEL  label that hands an issue to aoa (default: aoa)
  AOA_GH_ALLOW  logins trusted to author and label an issue, separated by spaces
                or commas (default: the login gh is authenticated as)
  AOA_BIN       aoa binary (default: aoa)

budget (ADR 017; `cycle` refuses to run without one):
  AOA_RUN_MAX_USD        dollars this cycle's `aoa run` may spend (required for cycle)
  AOA_RUN_MAX_GOALS      Goals that run may start (optional)
  AOA_MAX_GOALS_PER_CYCLE  new Goals intake submits per cycle (default: 1)
  AOA_MIN_QUOTA_PCT      skip the cycle unless the subscription windows quota-axi
                         reports have at least this much left (optional, needs quota-axi)
EOF
}

log() { printf 'aoa-github: %s\n' "$*" >&2; }
die() {
  log "$*"
  exit 1
}

# A call about one issue that fails skips that issue, not the run: one deleted
# issue must not stop every other from being reported. The script still exits 1
# at the end, so a scheduler can alert.
FAILED=0
skip() {
  log "#$1: $2; skipped until the next run"
  FAILED=1
}

# jq helpers for every filter below. Data always reaches jq through --arg.
# shellcheck disable=SC2016 # $names in here are jq's, not the shell's
JQ_DEFS='
# Whether nothing more happens to a goal without new input, read from its
# Complete condition (ADR 020) rather than a list of final outcomes. A goal
# cancelled while an attempt is still finishing is not complete yet.
def complete: any(.conditions[]?; .type == "Complete" and .status == "True");
def live: complete | not;

# The number of the issue a goal ref names, when it is an issue of repository $r.
def issue_of($r):
  ((.ref // "") | split("/")) as $p
  | if ($p | length) >= 5 and $p[-2] == "issues" and ($p[-1] | test("^[0-9]+$"))
       and (($p[-4] + "/" + $p[-3]) | ascii_downcase) == ($r | ascii_downcase)
    then $p[-1] else null end;

def same_label($l): ascii_downcase == ($l | ascii_downcase);

# Text from aoa quoted into a comment: absolute paths replaced, and nothing that
# could pass for one of our markers.
def unpath: gsub("(?<pre>^|[^A-Za-z0-9._~:/-])/[^\\s\"\\x27)\\],;:]+"; "\(.pre)<path>");
def unmark: gsub("<!--"; "<! --") | gsub("-->"; "-- >");
def reason:
  ([.tickets[] | select(.status == "failed") | .fail_reason // empty] + [.delivery_error // empty])
  | map(select(. != "")) | (first // "no reason recorded")
  | unpath | unmark
  | if length > 300 then .[:299] + "…" else . end;
def indent: split("\n") | map("    " + .) | join("\n");

# What the Goal cost, when the harness reported anything: the person deciding
# whether to keep pointing aoa at this repository is the person paying for it.
def spend:
  if (.cost_usd // 0) > 0 or (.tokens // 0) > 0 then
    "\n\nSpend: " + (if (.cost_usd // 0) > 0 then "$\(.cost_usd * 100 | round / 100)" else "unpriced" end)
    + ", \(.tokens // 0) tokens."
  else "" end;

def comment($label):
  (if .outcome == "delivered" then
    "aoa opened a pull request for this issue: \(.pr_url // "(no URL reported)")\n\nEvery commit on it passed the Gate. Review and merge it as usual."
  elif .outcome == "merged" then
    "aoa merged a verified change for this issue: \((.commits // []) | map(.[:12]) | join(", "))."
  elif .outcome == "failed" then
    "aoa could not complete this issue:\n\n\(reason | indent)\n\nTo try again, add the `\($label)` label again."
  elif .outcome == "cancelled" then
    "aoa cancelled its work on this issue. Nothing more from it will land."
  else
    "aoa has a verified change for this issue waiting for approval. Whoever operates the aoa workspace can land it with:\n\n\([.tickets[] | select(.status == "awaiting") | "aoa approve \(.id)"] | join("\n") | indent)"
  end)
  + spend
  + "\n\n<sub>aoa goal `\(.id)`</sub>\n<!-- aoa:\(.id):\(.outcome) -->";
'

# max_goals_per_cycle: how many new Goals one intake may submit. One by default:
# a front door that queues ten Goals at once has committed to ten Goals' spend.
max_goals_per_cycle() { printf '%s' "${AOA_MAX_GOALS_PER_CYCLE:-1}"; }

# budget_exhausted STATUS: whether the workspace's day budget is spent, as aoa
# reports it. Intake stops there: a Goal submitted now would only wait.
budget_exhausted() {
  jq -r '.budget.exhausted // false' <<<"$1"
}

# quota_ok: whether the vendor's own quota has the headroom AOA_MIN_QUOTA_PCT
# asks for. Subscription quota is the front door's business, not aoa's (ADR 015):
# aoa meters the spend it can see, and this keeps the fleet out of the quota the
# person at the keyboard is also using. Without quota-axi, or without the
# variable, it is not checked.
quota_ok() {
  local want left
  want=${AOA_MIN_QUOTA_PCT:-0}
  [[ $want == 0 ]] && return 0
  command -v quota-axi >/dev/null || { log "AOA_MIN_QUOTA_PCT is set but quota-axi is not installed"; return 1; }
  left=$(quota-axi --provider claude --json --no-credential-refresh 2>/dev/null |
    jq -r '[.providers[]?.windows[]?.remainingPercent // empty] | min // empty')
  if [[ -z $left ]]; then
    log "quota-axi reported no window (run 'quota-axi --allow-keychain-prompt' once); skipping the run"
    return 1
  fi
  if (( $(printf '%.0f' "$left") < want )); then
    log "quota left ${left}% is below AOA_MIN_QUOTA_PCT=${want}%; skipping the run"
    return 1
  fi
  return 0
}

# trusted LOGIN: whether LOGIN is in the allowlist. An empty login never is.
trusted() {
  local who
  who=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
  [[ -n $who && $ALLOW == *" $who "* ]]
}

# markers N: the markers in comments we wrote on issue N, as a JSON array of
# "goal:outcome". A marker in anyone else's comment is ignored: it could be forged.
markers() {
  gh api --paginate "repos/$REPO/issues/$1/comments" |
    jq -cs --arg me "$ME" '[add // [] | .[] | select(.user.login == $me) | .body // ""
      | scan("<!-- aoa:([^:\\s]+):([a-z_]+) -->") | join(":")] | unique'
}

# withdraw ISSUES STATUS: cancel every live github goal of this repository whose
# issue is no longer open with the label. Closing or unlabelling is the stop switch.
withdraw() {
  local issues=$1 status=$2 goal n ref listed view
  while IFS=$'\t' read -r goal n ref <&3; do
    listed=$(jq -r --arg u "$ref" 'any(.[]; .url == $u)' <<<"$issues")
    if [[ $listed == true ]]; then
      continue
    fi
    # The listing stops at 500 issues, so ask about this one before cancelling.
    if ! view=$(gh issue view "$n" --repo "$REPO" --json state,labels); then
      skip "$n" "could not read the issue"
      continue
    fi
    listed=$(jq -r --arg l "$LABEL" "$JQ_DEFS"'.state == "OPEN" and any(.labels[]; .name | same_label($l))' <<<"$view")
    if [[ $listed == true ]]; then
      continue
    fi
    if "$AOA" cancel --path "$AOA_WS" --json --by github --reason "issue closed or unlabelled" "$goal" >/dev/null; then
      log "#$n: cancelled goal $goal: the issue was closed or unlabelled"
    else
      # It settled after the snapshot was taken; report says how.
      log "#$n: could not cancel goal $goal"
    fi
  done 3< <(jq -r --arg r "$REPO" "$JQ_DEFS"'.goals[] | select(.source == "github" and live and .outcome != "cancelled")
    | issue_of($r) as $n | select($n != null) | [.id, $n, .ref] | @tsv' <<<"$status")
}

intake() {
  local issues status issue n url goal author labeller seen attempt text res
  issues=$(gh issue list --repo "$REPO" --state open --label "$LABEL" --limit 500 \
    --json number,title,body,url,author)
  status=$("$AOA" status --path "$AOA_WS" --json)
  withdraw "$issues" "$status"

  local left
  left=$(max_goals_per_cycle)
  if [[ $(budget_exhausted "$status") == true ]]; then
    log "the day's budget is spent; submitting nothing this cycle"
    left=0
  fi

  while IFS= read -r issue <&3; do
    n=$(jq -r '.number' <<<"$issue")
    url=$(jq -r '.url' <<<"$issue")
    goal=$(jq -r --arg u "$url" "$JQ_DEFS"'[.goals[] | select(.ref == $u and live)] | first | .id // ""' <<<"$status")
    if [[ -n $goal ]]; then
      continue # already in hand
    fi
    if (( left <= 0 )); then
      log "#$n: waiting for a later cycle (this one submits $(max_goals_per_cycle))"
      continue
    fi

    # Trust both whoever wrote the issue and whoever most recently applied the label.
    author=$(jq -r '.author.login // ""' <<<"$issue")
    if ! labeller=$(gh api --paginate "repos/$REPO/issues/$n/events" |
      jq -rs --arg l "$LABEL" "$JQ_DEFS"'[add // [] | .[] | select(.event == "labeled" and (.label.name // "" | same_label($l)))]
        | last | .actor.login // ""'); then
      skip "$n" "could not read its events"
      continue
    fi
    if ! trusted "$author" || ! trusted "$labeller"; then
      log "#$n: skipped: author '$author' and labeller '$labeller' must both be in AOA_GH_ALLOW"
      continue
    fi

    # Each attempt we reported as finished moves the key on, so a relabel is a retry.
    if ! seen=$(markers "$n"); then
      skip "$n" "could not read its comments"
      continue
    fi
    attempt=$(jq -r '[.[] | select(test(":(delivered|merged|failed|cancelled)$"))] | length' <<<"$seen")
    text=$(jq -r '.title + (if (.body // "") == "" then "" else "\n\n" + (.body | .[:8192]) end)' <<<"$issue")
    # The leading space keeps a title that starts with "-" from reading as a
    # flag. aoa trims it.
    if ! res=$("$AOA" goal --path "$AOA_WS" --json --source github --ref "$url" \
      --key "gh:$REPO#$n/$attempt" --by "$labeller" " $text"); then
      skip "$n" "aoa goal failed"
      continue
    fi
    goal=$(jq -r '.goal_id' <<<"$res")
    if [[ $(jq -r '.duplicate' <<<"$res") == true ]]; then
      log "#$n: attempt $attempt is already goal $goal"
    else
      log "#$n: submitted goal $goal (attempt $attempt, labelled by $labeller)"
      left=$((left - 1))
    fi
  done 3< <(jq -c '.[]' <<<"$issues")
}

report() {
  local status goal n outcome seen posted body
  status=$("$AOA" status --path "$AOA_WS" --json)
  while IFS=$'\t' read -r goal n outcome <&3; do
    if ! seen=$(markers "$n"); then
      skip "$n" "could not read its comments"
      continue
    fi
    posted=$(jq -r --arg m "$goal:$outcome" 'any(.[]; . == $m)' <<<"$seen")
    if [[ $posted == true ]]; then
      continue
    fi
    body=$(jq -r --arg g "$goal" --arg label "$LABEL" "$JQ_DEFS"'.goals[] | select(.id == $g) | comment($label)' <<<"$status")
    if [[ $outcome != awaiting_approval ]]; then
      # Unlabel before the final comment, so that trying again takes a human
      # adding the label back.
      if ! gh issue edit "$n" --repo "$REPO" --remove-label "$LABEL" >/dev/null; then
        skip "$n" "could not remove the $LABEL label"
        continue
      fi
      log "#$n: removed the $LABEL label"
    fi
    if ! gh issue comment "$n" --repo "$REPO" --body "$body" >/dev/null; then
      skip "$n" "could not comment"
      continue
    fi
    log "#$n: reported goal $goal: $outcome"
  done 3< <(jq -r --arg r "$REPO" "$JQ_DEFS"'.goals[] | select(.source == "github" and (complete or .outcome == "awaiting_approval"))
    | issue_of($r) as $n | select($n != null) | [.id, $n, .outcome] | @tsv' <<<"$status")
}

cycle() {
  # A cycle is automation, and automation runs on a budget or not at all
  # (ADR 017). The run's own limits are aoa's to enforce; this only refuses to
  # start one without them.
  [[ -n ${AOA_RUN_MAX_USD:-} ]] ||
    die "set AOA_RUN_MAX_USD: a cycle runs on a budget (for example AOA_RUN_MAX_USD=3)"
  quota_ok || return 0
  intake
  local rc=0 run_args=(run --path "$AOA_WS" --max-usd "$AOA_RUN_MAX_USD")
  [[ -n ${AOA_RUN_MAX_GOALS:-} ]] && run_args+=(--max-goals "$AOA_RUN_MAX_GOALS")
  "$AOA" "${run_args[@]}" >&2 || rc=$?
  case $rc in
    0) ;;
    75) log "aoa run: another Scheduler holds the workspace and will pick the goals up" ;;
    1) log "aoa run exited 1: a task failed, a delivery is pending, or it could not start" ;;
    *) log "aoa run exited $rc" ;;
  esac
  report
}

if [[ $# -ne 1 ]]; then
  usage >&2
  exit 2
fi
case $1 in
  intake | report | cycle) ;;
  -h | --help | help)
    usage
    exit 0
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac

[[ -n ${AOA_WS:-} ]] || die "AOA_WS is required: the aoa workspace to drive"
[[ ${AOA_GH_REPO:-} =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || die "AOA_GH_REPO is required, as owner/name"
REPO=$AOA_GH_REPO
LABEL=${AOA_GH_LABEL:-aoa}
AOA=${AOA_BIN:-aoa}
for bin in gh jq "$AOA"; do
  command -v "$bin" >/dev/null || die "$bin is not on PATH"
done
ME=$(gh api user --jq .login) || die "gh api user failed: is gh authenticated? (gh auth status)"
[[ -n $ME ]] || die "gh api user returned no login"
ALLOW=" $(printf '%s' "${AOA_GH_ALLOW:-$ME}" | tr ',\t\n' '   ' | tr '[:upper:]' '[:lower:]') "

case $1 in
  intake) intake ;;
  report) report ;;
  cycle) cycle ;;
esac
exit "$FAILED"
