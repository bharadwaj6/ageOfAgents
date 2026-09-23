# Antigravity CLI (agy)

**Status: verified on macOS (agy 1.2.8); unverified elsewhere.**

There is no built-in preset and no Go code for `agy` in `aoa`. Headless `agy` only works with
auto-approval (`--dangerously-skip-permissions`), and on 2026-09-23 it left its worktree and pushed
straight to `main` when run unconfined. Therefore, `agy` is documented and run as a
[BYO CLI harness](byo-cli.md) under operator-side confinement.

## What aoa runs (Flags)

Headless invocation:

```
agy --dangerously-skip-permissions --mode accept-edits --add-dir . --output-format json -p "<prompt>"
```

Why each flag is required:

- **`--dangerously-skip-permissions`**: Without auto-approval, headless `agy` auto-denies file reads and
  shell commands it cannot prompt for. The JSON envelope records these in `denied_actions`, causing the
  attempt to fail.
- **`--mode accept-edits`**: A user's local `agy` settings may default `agentMode` to `plan`, which
  inspects the repository but never edits files.
- **`--add-dir .`**: Without this, `agy` operates in its own scratch project
  (`~/.gemini/antigravity-cli/scratch`) instead of the current working directory.
- **`--output-format json`**: Emits a structured JSON envelope on stdout that `aoa` parses.
- **`-p`**: Passes the prompt as a flag argument, ensuring the prompt is handed directly to the agent.

**Do not pass `--sandbox` under an outer sandbox:** It cannot start there (`sandbox_apply: Operation not permitted`),
and `agy` retries the command with its own sandbox bypassed, so it is not a boundary.

## Output and cost

`--output-format json` prints a single envelope upon completion:

- Prose response is in `response`.
- Token usage is in `usage.total_tokens`.

`aoa`'s parser reads `usage.total_tokens`, so tokens are accounted for and charged against token budgets.
However, `agy` reports neither dollar cost nor model name in its envelope. Bound runs using token
governors instead of dollar limits:

- `max_tokens_per_goal` in `aoa.toml`
- `aoa run --max-tokens <n>` on the command line

## Why it must be confined (the incident)

`agy` loads the user's global agent instructions (`~/.gemini/GEMINI.md`) and provides no flag to
exclude them.

Run unconfined on 2026-09-23, an agent `aoa` launched for
[#186](https://github.com/bharadwaj6/ageOfAgents/issues/186) edited the user's canonical checkout
instead of its worktree and pushed a commit straight to `main`, following global instructions to
commit and push.

A git worktree is a working directory, not a security boundary
([ADR 018](../design/adr/018-the-agent-is-not-confined.md)). Because headless `agy` requires
`--dangerously-skip-permissions` to work unattended, the operator must confine it externally.

## The confinement recipe

Confinement is applied on the operator side on macOS using `sandbox-exec`. Adapt the paths for your
machine.

### The Seatbelt profile (`agy.sb`)

Save the following profile as `agy.sb`. It is reproduced verbatim:

```scheme
(version 1)
(allow default)
(deny file-write*
  (require-all
    (require-not (subpath (param "ROOT")))
    (require-not (subpath (param "GEMINI")))
    (require-not (subpath (param "CACHES")))
    (require-not (subpath (param "APPSUPPORT")))
    (require-not (subpath (param "GOPATH")))
    (require-not (subpath "/private/var/folders"))
    (require-not (subpath "/private/tmp"))
    (require-not (subpath "/dev"))))
(deny process-exec (regex #"/gh$"))
(deny process-exec (regex #"/git-remote-(https?|ftps?|ext|fd)$") (regex #"/git-credential-[^/]*$") (regex #"/ssh$"))
```

How this profile works:

- **Writes are denied outside the clone and its worktrees**, plus `agy`'s state, caches and temp files:
    - `ROOT`: The repository clone root and all worktrees under `.aoa/worktrees/`.
    - `GEMINI`: `$HOME/.gemini` (state, settings, logs).
    - `CACHES`: `$HOME/Library/Caches`.
    - `APPSUPPORT`: `$HOME/Library/Application Support/Antigravity`.
    - `GOPATH`: `$(go env GOPATH)` (Go build caches and modules).
    - Temp and devices: `/private/var/folders`, `/private/tmp`, `/dev`.
- **`gh` is denied by a regex**: `/opt/homebrew/bin/gh` is a symlink, and a `literal` rule does not
  match the resolved target path. A regex (`#"/gh$"`) matches the executed binary name directly.
- **Network git is denied**: `aoa` pushes the branch itself, from outside the sandbox. Without this
  rule, a credential manager on the machine lets the agent push.
- **Reads stay open**: The agent can inspect system headers, toolchains, and repository files.

### The wrapper script

A wrapper script refuses to run outside the agent's own clone, then execs `agy` under `sandbox-exec`:

```bash
#!/usr/bin/env bash
set -euo pipefail

# The clone's physical path, with no trailing slash (resolve it once with `pwd -P`).
CLONE_ROOT="/path/to/your/clone"
# Compare physical paths, with the trailing slash: a bare prefix match would also
# admit a sibling such as /path/to/your/clone-other, and $PWD can be a symlink.
case "$(pwd -P)/" in
  "$CLONE_ROOT"/*) ;;
  *)
    echo "Refusing to run agy outside $CLONE_ROOT (current: $(pwd -P))" >&2
    exit 1
    ;;
esac

exec sandbox-exec -f /path/to/agy.sb \
  -D ROOT="$CLONE_ROOT" \
  -D GEMINI="$HOME/.gemini" \
  -D CACHES="$HOME/Library/Caches" \
  -D APPSUPPORT="$HOME/Library/Application Support/Antigravity" \
  -D GOPATH="$(go env GOPATH 2>/dev/null || echo "$HOME/go")" \
  agy --dangerously-skip-permissions --mode accept-edits --add-dir . --output-format json -p "$@"
```

### Configuring `aoa.toml`

Point `[backends.agy] bin` at the wrapper:

```toml
backend = "agy"
conventions_file = ".aoa/standing-instructions.txt"

[backends.agy]
type = "cli"
bin  = "/path/to/agy-wrapper"
args = []
```

### Standing instructions and branch protection

Two additional safeguards are necessary:

1. **Standing instructions via `conventions_file`**: Give the agent the orchestrator's standing
   instructions (work only in the working directory; never commit, push, or run `gh`). If the agent
   commits, `aoa` sees a clean tree and reports `"agent produced no changes"`. Note that since
   [#196](https://github.com/bharadwaj6/ageOfAgents/issues/196) an unreadable `conventions_file` fails the
   run immediately instead of silently dropping the instructions.
2. **Branch protection**: Make `main`'s branch protection bind administrators ("Do not allow bypassing
   the above settings"), so no credentials reachable by an agent can push straight to `main`.

## The headless waiting trap

About half of `agy`'s attempts ended with no edits at all.

Transcripts showed why: before editing, `agy` started `make check && golangci-lint run` "to verify the
baseline". Because the command ran long enough to go to the background, `agy` said "I will wait for it to
complete". In headless mode (`-p`), stopping to wait ends the turn, so the process exited with nothing
written and `aoa` recorded `"agent produced no changes"`.

The orchestrator's standing instructions must instruct the agent to make its edits first, never start a
command in the background or stop to wait for one, and leave `make check`/`golangci-lint` to the Gate.

## Verification status, honestly

Under that confinement:

- `agy` delivered two Gate-verified pull requests:
    - [#199](https://github.com/bharadwaj6/ageOfAgents/pull/199) for [#196](https://github.com/bharadwaj6/ageOfAgents/issues/196)
    - [#200](https://github.com/bharadwaj6/ageOfAgents/pull/200) for [#198](https://github.com/bharadwaj6/ageOfAgents/issues/198)
- [#197](https://github.com/bharadwaj6/ageOfAgents/issues/197) failed four attempts, all ending without
  an edit — the same pattern, though only one failed attempt's transcript was read to confirm the
  waiting trap as the cause — and was done by the `claudecode` backend instead
  ([#201](https://github.com/bharadwaj6/ageOfAgents/pull/201)).
- This page ([#188](https://github.com/bharadwaj6/ageOfAgents/issues/188)) was written by `agy` under the
  same confinement. Its first two attempts fell into the waiting trap; the first attempt after the
  standing instructions were changed succeeded. One success is a data point, not a rate.
- Pushes, writes outside the clone, and `gh` were all confirmed blocked in testing.
