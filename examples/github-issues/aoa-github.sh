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
def live: .outcome == "queued" or .outcome == "running" or .outcome == "awaiting_approval";
def terminal: .outcome == "delivered" or .outcome == "merged" or .outcome == "failed" or .outcome == "cancelled";

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
  + "\n\n<sub>aoa goal `\(.id)`</sub>\n<!-- aoa:\(.id):\(.outcome) -->";
'

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
  done 3< <(jq -r --arg r "$REPO" "$JQ_DEFS"'.goals[] | select(.source == "github" and live)
    | issue_of($r) as $n | select($n != null) | [.id, $n, .ref] | @tsv' <<<"$status")
}

intake() {
  local issues status issue n url goal author labeller seen attempt text res
  issues=$(gh issue list --repo "$REPO" --state open --label "$LABEL" --limit 500 \
    --json number,title,body,url,author)
  status=$("$AOA" status --path "$AOA_WS" --json)
  withdraw "$issues" "$status"

  while IFS= read -r issue <&3; do
    n=$(jq -r '.number' <<<"$issue")
    url=$(jq -r '.url' <<<"$issue")
    goal=$(jq -r --arg u "$url" "$JQ_DEFS"'[.goals[] | select(.ref == $u and live)] | first | .id // ""' <<<"$status")
    if [[ -n $goal ]]; then
      continue # already in hand
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
  done 3< <(jq -r --arg r "$REPO" "$JQ_DEFS"'.goals[] | select(.source == "github" and (terminal or .outcome == "awaiting_approval"))
    | issue_of($r) as $n | select($n != null) | [.id, $n, .outcome] | @tsv' <<<"$status")
}

cycle() {
  intake
  local rc=0
  "$AOA" run --path "$AOA_WS" >&2 || rc=$?
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
