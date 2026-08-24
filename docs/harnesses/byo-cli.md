# BYOHarness — drive any CLI

If your coding agent is a command-line tool that edits files in place, `aoa` can drive it with no Go
code and no release. That is `type = "cli"`.

```toml
backend = "mycoder"

[backends.mycoder]
type = "cli"
bin  = "mycoder"
args = ["run", "--yes", "--format", "json"]
```

`aoa` runs:

```
mycoder run --yes --format json "<prompt>"
```

## The one rule

**`args` are passed verbatim, then the prompt is appended as the final argument.**

That covers both conventions in the wild. If your CLI takes the prompt as a flag value, put the flag
last: `args = ["--prompt"]` gives `mycoder --prompt "<prompt>"`. If it takes a positional, just don't.

The prompt is one element of an argv array handed straight to `exec`, **never a shell string**. A Goal
containing backticks, `$(...)`, quotes or newlines is passed through literally rather than executed.

### If your CLI needs the prompt somewhere else

Wrap it. `aoa` deliberately has no template syntax for this:

```bash
#!/usr/bin/env bash
# ~/bin/mycoder-aoa — prompt in the middle
exec mycoder --prompt "$1" --workdir . --yes
```

```toml
[backends.mycoder]
type = "cli"
bin  = "mycoder-aoa"
```

## What your CLI must do

- **Edit files in the current working directory.** `aoa` runs it inside a throwaway git worktree and
  snapshots whatever changed. Don't commit, push, or open a PR — `aoa` does that, after the Gate passes.
- **Run headless.** Anything that waits for a keypress will hang until `agent_timeout`. Most CLIs need
  a flag for this (`--yes`, `--force`, `--approval-mode`, `--permission-mode`); find yours.
- **Exit 0 on success.** A non-zero exit is recorded as a failed attempt and retried up to
  `max_attempts`.

Its stdout is kept as the attempt's trace in the Event Log. It does not need to be structured.

## Cost accounting

A BYO CLI reports no tokens, so `max_tokens_per_goal` and `max_usd_per_goal` cannot be enforced. `aoa`
warns at startup rather than silently reporting `$0`.

To opt in, have the agent print a fence anywhere in its output:

````
```aoa:usage
{"tokens": 4321, "model": "mycoder-1"}
```
````

`aoa` also recognises a few common JSON envelope shapes automatically — if your CLI prints a single
JSON object with `result`, `text` or `response`, and a `usage` block, you get real counts for free.

## Overriding a built-in

A `[backends.<name>]` block **shadows a built-in of the same name**. This is the supported way to fix a
preset whose flags have moved:

```toml
backend = "codex"

[backends.codex]
type = "cli"
bin  = "codex"
args = ["exec", "--json", "--sandbox", "workspace-write"]
```

## Check it

```bash
aoa doctor --path ./workspace
```

confirms `bin` is on `$PATH` before you spend a retry budget finding out.
