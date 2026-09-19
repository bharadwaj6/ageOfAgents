# ADR 016: Deliver a Goal as a Pull Request

## Status
Accepted. Extends [ADR 002](002-verifier-gated-merge-queue.md) (what the Gate guarantees) and
[ADR 015](015-aoa-is-a-backend.md) §9 ("lands" means the integration branch, for now).

## Context
Today a verified change lands on whichever branch the adopted repository has checked out. Nothing is
pushed and no pull request is opened. That works for a personal repository aoa owns outright. It does
not work for a team repository, where `main` is protected and changes arrive as reviewed PRs. aoa's
own repository is like that: `main` requires three checks in strict up-to-date mode. So aoa cannot yet
do the thing ADR 015 positions it for: take an issue from a front door and hand back a change a team
can merge.

There are two shapes for delivery. **Push** merges locally and pushes `main`. That is useless against
a protected branch, and dangerous where branch protection does not bind administrators: aoa running
with an owner's credentials could push past the required checks. **Pull request** hands the verified
change to the forge, whose checks and merge rules decide the final landing. Only the second fits the
repositories that need it.

The unit of delivery is the harder question. A per-ticket PR looks natural, but it breaks emergent
decomposition ([ADR 006](006-emergent-task-graph-blackboard.md)). A child task that depends on a sibling
needs the sibling's code in its base. Per-ticket PRs would give it either a base without that code or
an automatically stacked chain of PRs. Stacked PRs fall apart under rebase-merge, which rewrites every
commit.

## Decision
**Deliver each Goal as one pull request, from a branch the Gate has verified at every step.**

1. **`[delivery] mode = "pr"`** (the default stays `"local"`, today's behaviour). No push mode.
2. **One Goal, one branch, one PR.** Before a Goal's first dispatch, aoa fetches `<remote>/<base>` and
   cuts `aoa/<goal-id>` from it. Every ticket worktree for the Goal is cut from that branch, so a child
   sees the work its siblings already merged into it. One issue becomes one Goal, which becomes one PR:
   the shape a front door wants.
3. **The Gate still runs on the post-merge state, on the Goal branch.** For each merge-queue step aoa
   detaches onto the Goal branch and runs the unchanged verify → merge → roll back. Only a pass moves
   the branch, with a compare-and-swap `git update-ref <new> <old>`. So the Goal branch only ever points
   at commits the Gate passed. The adopted repository's checked-out branch and the remote base are
   never written.
4. **What aoa guarantees changes shape.** It becomes *every commit aoa pushes passed the Gate*.
   *`main` stays green* becomes the forge's job, through its required checks and up-to-date rule.
5. **Delivery happens once the Goal is complete.** When every ticket of the Goal is complete, aoa:
   1. pushes `aoa/<goal-id>`, never with force;
   2. runs a configurable **opener command** that opens the PR;
   3. records `Delivered{goal_id, branch, commit, url}`.

   A failed or partial Goal is never pushed.
6. **aoa stops once the PR is open.** It does not re-gate against a base that moved later, follow the
   PR, or merge it. Review and landing belong to the forge and its people.
7. **The opener is configuration, not code**, in the style of [ADR 014](014-cli-backends-as-data.md).
   - The default is an argv that runs `gh pr view` and falls back to `gh pr create`, which makes it
     idempotent.
   - The placeholders `{branch} {base} {title} {body}` are passed as separate arguments, never spliced
     into a shell string.
   - Another forge is a configuration change (`glab`, `tea`), not a code change.
   - `gh` is not a Go dependency. It is needed only by those who opt into PR mode and keep the default
     opener.
8. **A failure is recorded, not hidden.** A push or opener failure appends
   `DeliveryFailed{goal_id, branch, reason}` and is retried on the next `aoa run`, at most once per run.
   A remote branch holding a foreign commit is refused, never overwritten.
9. **Crash safety comes from determinism.** The branch name is a function of the Goal, pushes are
   never forced, and the opener checks for an existing PR before creating one. Re-running after a crash
   at any step yields exactly one PR.

## Consequences
- **The replay side gains three things:** the events `Delivered` and `DeliveryFailed`, a `branch` on
  `Merged`, and a `delivered` goal outcome. Tickets keep their existing statuses: they merged, onto the
  Goal branch.
- **A new invariant** checks that delivery happens at most once per Goal, only after every ticket is
  complete, and only for the Goal branch's last verified commit.
- **Batching of disjoint-file proposals is off in PR mode,** because a batch would span Goal branches.
- **Two things become redundant or unsupported.** `require_approval` still works but is usually
  redundant, since PR review is the human checkpoint. Dependencies between Goals are unsupported.
- **The TLA+ model covers local mode only.** Where `action.yml` keeps its log between runs is still
  open ([#128](https://github.com/bharadwaj6/ageOfAgents/issues/128)).
- **Tradeoff.** The Goal branch is cut once, so a long Goal can drift from a fast-moving base. The
  forge's up-to-date rule catches that at merge time, not aoa.

## Research basis
Engineering judgement about where to draw the delivery boundary, not a paper. The Gate's case is ADR
002's and is unchanged. What changes is which branch the Gate guards, and who guards `main`.
