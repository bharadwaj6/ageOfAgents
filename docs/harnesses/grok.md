# Grok (xAI)

**Status: verified on grok CLI 1.0.41 on macOS on 2026-09-23.** This is the backend the loop was
first proved end to end on. The built-in preset is unconfined. The recipe below is an operator-side
boundary for macOS; it was checked on that version and that date.

```toml
backend = "grok"
```

Needs the `grok` CLI on `$PATH`, logged in at grok.com. **No API key.**

## What aoa runs

The built-in preset runs:

```
grok --permission-mode bypassPermissions --output-format json -p "<prompt>"
```

`bypassPermissions` is required. Without a permission mode the CLI runs but declines to write files,
and every Task fails with "agent produced no changes".

A measured run pins the model and the reasoning effort as well, so a change to the user's grok
config cannot change what the run measures. That command runs one turn and exits:

```
grok --permission-mode bypassPermissions --output-format json -m <model> --reasoning-effort <effort> -p "<prompt>"
```

`grok models` on 1.0.41 lists `grok-4.7` (the default), `grok-4.7-build-fast`, `grok-4.6` and
`grok-4.5`. Pass one of those to `-m`. The [wrapper](#the-wrapper-script) below pins both flags.

**Leader daemon.** On 1.0.41 a headless run had no leader process at all: `grok leader list` showed
none before the run and none after, and the vendor's README says leader mode is off by default.

aoa's built-in `grok` preset still starts a leader daemon on first use. It checks `grok leader list`
once per run and spawns `grok agent leader` when none is reachable. That daemon outlives `aoa` by
design.

A `[backends.grok]` block with `type = "cli"` [shadows the preset](byo-cli.md#overriding-a-built-in)
and skips that step. The recipe below is such a block, so the confined run does not start an
unconfined leader beside it.

## Output and cost

`--output-format json` prints one envelope. It carries:

- `usage.total_tokens`
- a per-model `modelUsage` block naming the model
- `total_cost_usd`

`aoa` reads the tokens and the model, and charges the envelope's `total_cost_usd` rather than
`[pricing]`.

Under a grok.com login that figure is the vendor's own estimate. Bound runs with
`max_tokens_per_goal` ([Configuration](../config-reference.md)).

## Why confine it

`grok` loads the user's global instructions from `$HOME/.grok/AGENTS.md` and has no flag to exclude
them, the same way `agy` loads `$HOME/.gemini/GEMINI.md`. The [agy page](agy.md) describes what that
led to on [#186](https://github.com/bharadwaj6/ageOfAgents/issues/186): an unconfined agent left its
worktree and pushed to `main`.

`grok`'s own `--sandbox` profiles apply Seatbelt to the whole process. They are not used here,
because the outer profile below is the boundary.

A git worktree is a working directory, not a security boundary
([ADR 018](../design/adr/018-the-agent-is-not-confined.md), [Security](../security.md)). Confinement
is the operator's, on the machine `aoa` runs on.

## The confinement recipe

Operator-side, on macOS, with `sandbox-exec`. Keep the wrapper and the profile **outside** the clone.
The profile allows writes under the clone, so a copy stored inside it can be rewritten by the agent
and the next attempt would run whatever it wrote.

### The Seatbelt profile (`grok.sb`)

Save this outside the clone, for example at `$HOME/bin/grok.sb`. In the leader-socket rule, replace
`<$HOME>` with the absolute home directory: `path-literal` is matched literally, and the shell does
not expand the profile.

```scheme
(version 1)
(allow default)
(deny file-write*
  (require-all
    (require-not (subpath (param "ROOT")))
    (require-not (subpath (param "GROK")))
    (require-not (subpath (param "CACHES")))
    (require-not (subpath (param "CACHE")))
    (require-not (subpath (param "GOPATH")))
    (require-not (subpath "/private/var/folders"))
    (require-not (subpath "/private/tmp"))
    (require-not (subpath "/dev"))))
(deny process-exec (regex #"/gh$"))
(deny process-exec (regex #"/git-remote-(https?|ftps?|ext|fd)$") (regex #"/git-credential-[^/]*$") (regex #"/ssh$"))
(deny network-outbound (remote unix-socket (path-literal "<$HOME>/.grok/leader.sock")))
```

How this profile works:

- **Writes are allowed only** under the clone root and its worktrees (`ROOT`, `<clone root>`),
  `$HOME/.grok` (`GROK`), `$HOME/Library/Caches` (`CACHES`), `$HOME/.cache` (`CACHE`), the Go module
  path (`GOPATH`, `$(go env GOPATH)`), `/private/var/folders`, `/private/tmp` and `/dev`.
- **`gh` is denied** by `(deny process-exec (regex #"/gh$"))`. A `literal` rule misses a symlink's
  resolved target; the regex matches the executed name.
- **Network git is denied** by
  `(deny process-exec (regex #"/git-remote-(https?|ftps?|ext|fd)$") (regex #"/git-credential-[^/]*$") (regex #"/ssh$"))`.
  `aoa` pushes the branch itself, from outside the sandbox. Without this rule, a credential manager
  on the machine lets the agent push.
- **The shared leader socket is denied** by
  `(deny network-outbound (remote unix-socket (path-literal "<$HOME>/.grok/leader.sock")))`, so a
  confined client can never hand its tools to an unconfined leader.
- **Reads stay open**, and network other than that socket stays open. This bounds writes, `gh`,
  network git and the leader socket. It is not a jail, and `aoa` itself runs outside it.

### The wrapper script

The wrapper refuses to run outside the agent's own clone, then execs `grok` under `sandbox-exec`.
Save it outside the clone, for example at `$HOME/bin/grok-wrapper`, and replace `<clone root>` and
`<effort>` before use.

```bash
#!/usr/bin/env bash
set -euo pipefail

# The clone's physical path, with no trailing slash (resolve it once with `pwd -P`).
CLONE_ROOT="<clone root>"
# Pin both, so a change to the user's grok config cannot change what a run measures.
MODEL="grok-4.7"
REASONING_EFFORT="<effort>"
# Compare physical paths, with the trailing slash: a bare prefix match would also
# admit a sibling such as <clone root>-other, and $PWD can be a symlink.
case "$(pwd -P)/" in
  "$CLONE_ROOT"/*) ;;
  *)
    echo "Refusing to run grok outside $CLONE_ROOT (current: $(pwd -P))" >&2
    exit 1
    ;;
esac

exec sandbox-exec -f "$HOME/bin/grok.sb" \
  -D ROOT="$CLONE_ROOT" \
  -D GROK="$HOME/.grok" \
  -D CACHES="$HOME/Library/Caches" \
  -D CACHE="$HOME/.cache" \
  -D GOPATH="$(go env GOPATH 2>/dev/null || echo "$HOME/go")" \
  grok --permission-mode bypassPermissions --output-format json \
    -m "$MODEL" --reasoning-effort "$REASONING_EFFORT" -p "$@"
```

`aoa` appends the prompt as the final argument. The wrapper passes it to `-p`.

### Configuring `aoa.toml`

Point `[backends.grok] bin` at the wrapper. Write `bin` as an absolute path: `aoa` does not expand
`$HOME`.

```toml
backend = "grok"

[backends.grok]
type = "cli"
bin  = "/path/to/grok-wrapper"
args = []
```

The block shadows the built-in preset, so the leader daemon is not started. The wrapper's file name
is not `grok` — the script runs `grok` from `$PATH`, and a wrapper of the same name would exec
itself — so `aoa` reports the backend as a BYO harness. With `max_tokens_per_goal` set it says usage
is read from the output. The envelope above is one it recognises, so the tokens, the model and
`total_cost_usd` are still charged.

## What was checked

On grok CLI 1.0.41 on macOS on 2026-09-23, a headless run under this profile could write in its
clone. Writing into another checkout, running `gh`, and `git ls-remote` over https were each refused
with "Operation not permitted".
