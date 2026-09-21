# ADR 018: The Agent Is Not Confined, and Says So

## Status
Accepted. Resolves [#101](https://github.com/bharadwaj6/ageOfAgents/issues/101). Bounds the scope of
[ADR 002](002-verifier-gated-merge-queue.md)'s Gate and explains why
[#71](https://github.com/bharadwaj6/ageOfAgents/issues/71) (microVM isolation) stays deferred.

## Context
`aoa` runs code a language model wrote, then runs the project's build and tests on the result. Both are
arbitrary code execution. The question left open was whether `aoa` should *confine* the first one, and
the answer was being given by default rather than by decision.

What is true today, read off the code rather than assumed:

- **The worktree is not a boundary.** The `openai` and `anthropic` backends run the model's commands
  with `exec.CommandContext(ctx, "bash", "-c", …)` and `cmd.Dir = task.Worktree`. `cmd.Dir` is the
  directory the command *starts* in; nothing holds it there. The command can `cd /`, read `~/.ssh`,
  reach the network, or `git push`.
- **The CLI harnesses are launched in permissive modes on purpose** — `acceptEdits`,
  `bypassPermissions`, `--force --trust`, `--approval-mode yolo`. Without them a headless run writes no
  files and every Task fails with "agent produced no changes". `codex` is the exception: `codex exec
  --sandbox workspace-write` is the harness's own OS sandbox, and it is codex's, not `aoa`'s.
- **The agent inherits the environment.** No backend sets `cmd.Env`, so every exported key and token in
  the launching shell is visible to the agent process.
- **`sandbox = "docker"` containerises the Gate only.** It wraps each `verify` command in `docker run`
  with the repository bind-mounted read-write. It never touches the agent, and even for the Gate it is a
  boundary against host accidents, not a jail: network is on and the default image runs as root.

The tempting fix is that last bullet's plumbing: `Verifier.Sandbox`/`Image` already exist, so route the
agent through the same container. It does not survive contact with how the harnesses authenticate. The
CLI backends are authenticated by credential stores in the user's home directory (`~/.claude`, a
grok.com login). Running them in the Gate's container means mounting those credentials into it — which
reintroduces the exposure the container was supposed to remove, while adding an image that must carry
every agent CLI. For the native `openai`/`anthropic` backends, containerising the `bash` tool is more
plausible, but it is a real feature with a lifecycle, not a reuse of existing plumbing, and it would
confine only the two backends nobody has verified against a live API.

Meaningful confinement — seccomp, namespaces, a restricted shell, a microVM — is a large, platform-specific
change that buys a required external service and contradicts the standing commitment to one static
binary, one config file, git only (golden rule 6, [ADR 012](012-observability-as-replay-projection.md)).

## Decision
**`aoa` does not confine the agent. It states that prominently instead of implying otherwise.**

1. **Confinement is the harness's business or the operator's, not the core's.** Where a harness confines
   itself (codex), that is a reason to choose it, documented as the harness's property. Where the
   operator wants confinement, the answer is the machine `aoa` runs on: a VM, a container, a dedicated
   box. This is the same answer every coding agent gives, and it is the one that actually works.
2. **The Gate is the safety property `aoa` does provide, and it is about correctness, not containment.**
   Nothing merges that fails the build and tests. That bounds what reaches `main`. It does not bound
   what the agent did to the machine on the way there, and the docs must not let those two be confused.
3. **The posture is stated where someone meets it, not only in a policy file.** A dedicated Security
   page in the published docs, the README, the getting-started path, and a `confinement` check in
   `aoa doctor` that names the configured backend and what it can reach.
4. **`aoa doctor` warns, never fails, on this.** It is the documented design, so making it a failure
   would train people to ignore the doctor and break it in CI for everyone.
5. **No reassuring language.** Describing `sandbox = "docker"` without saying it covers the Gate only is
   the bug this ADR closes. The same applies to the worktree, which isolates agents from each other and
   is not a security boundary.

## Consequences
- **`aoa` is honest about being unsuitable for running untrusted or adversarial goals.** That is a real
  limitation, not a disclaimer: a goal whose text an attacker controls should not be run on a machine
  that matters. Front doors ([ADR 015](015-aoa-is-a-backend.md)) that accept public input — `aoa serve`
  with a widened `--allow`, for instance — inherit this and must say so.
- **`#71` (Firecracker microVMs) stays deferred**, and this ADR is the reason to point at. It is captured,
  not rejected: a metric that justifies it — a real deployment taking untrusted goals — would reopen it,
  and it would come back as isolation of the whole run, not a flag.
- **A new `confinement` check in `aoa doctor`**, warning by default on any non-`mock` backend.
- **Rejected alternatives:**
  - Routing the agent through the Gate's docker sandbox. Breaks CLI authentication, or restores the
    exposure by mounting credentials into the container.
  - A restricted shell or an allowlist of commands for the native backends' `bash` tool. Trivially
    escaped (any interpreter on the image defeats it) and a false sense of safety is worse than none.
  - Stripping the environment before launching a harness. The CLI harnesses need their credentials to
    work at all, so this would confine only the backends that read a key from the environment — the ones
    where the key is the point.
- **Tradeoff.** This decision is cheap for `aoa` and moves the cost to the operator. That is defensible
  only while it is said plainly and early, which is what point 3 is for. If the docs drift back toward
  reassurance, this ADR has been violated even though no code changed.

## Research basis
Engineering judgement and a reading of the tree, not external evidence. The one empirical input is that
every comparable headless coding agent takes the same position — the sandbox is the machine you run it
on — and the one harness that does better (codex) does it inside its own CLI, which is exactly where
this ADR says it belongs.
