#!/usr/bin/env bash
# Put a precision-runner task repository back on a recorded base commit.
#
# aoa eval merges a passing change into the repository's main, and a rejected
# attempt leaves its aoa/* branch checked out in a preserved worktree. The next
# eval on this checkout has to start from the same pristine commit as the first.
#
# Usage: gate_precision_reset.sh REPO BASE_SHA
#
# Prints nothing on success. Exits non-zero with a message on stderr when REPO
# is not a git repository or BASE_SHA is not a commit in it.
set -euo pipefail

if [[ $# -ne 2 ]]; then
    echo "error: usage: gate_precision_reset.sh REPO BASE_SHA" >&2
    exit 1
fi

REPO="$1"
BASE_SHA="$2"

if ! git -C "$REPO" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    echo "error: not a git repository: $REPO" >&2
    exit 1
fi

if ! git -C "$REPO" cat-file -e "${BASE_SHA}^{commit}" >/dev/null 2>&1; then
    echo "error: not a commit in $REPO: $BASE_SHA" >&2
    exit 1
fi

git -C "$REPO" checkout --quiet --force main
git -C "$REPO" reset --hard --quiet "$BASE_SHA"
git -C "$REPO" clean --quiet -fdx

# A preserved aoa worktree keeps its branch checked out, so the branch cannot
# be deleted until that worktree is gone. Compare physical paths: a linked
# checkout and `rev-parse --show-toplevel` can disagree on a symlinked prefix.
primary="$(git -C "$REPO" rev-parse --show-toplevel)"
primary="$(cd "$primary" && pwd -P)"
while IFS= read -r line; do
    case "$line" in
        worktree\ *) wt="${line#worktree }" ;;
        *) continue ;;
    esac
    wt_phys="$(cd "$wt" && pwd -P)"
    if [[ -z "$wt_phys" || "$wt_phys" == "$primary" ]]; then
        continue
    fi
    git -C "$REPO" worktree remove --force "$wt" >/dev/null
done < <(git -C "$REPO" worktree list --porcelain)

while IFS= read -r branch; do
    if [[ -z "$branch" || "$branch" == "main" ]]; then
        continue
    fi
    git -C "$REPO" branch -D "$branch" >/dev/null
done < <(git -C "$REPO" for-each-ref --format='%(refname:short)' refs/heads)
