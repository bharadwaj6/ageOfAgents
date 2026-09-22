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

Usage for a BYO CLI is read from its output if it prints a JSON envelope `aoa` recognises or an
`aoa:usage` fence; `aoa status` after a first run shows whether it was charged. `aoa` warns at startup
since it cannot know until a run whether counts will be reported.

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

An override that keeps the preset's `bin` keeps its cost accounting: token counts are read off the
harness's own output envelope, not off the preset table, so the spend governors stay live — as long as
your `args` keep whatever flag makes it print that envelope (`--json` here, `--output-format json` for
claude and grok). Point `bin` at something else, a wrapper script say, and `aoa` treats it as a BYO
harness again, because it is.

## Check it

```bash
aoa doctor --path ./workspace
```

confirms `bin` is on `$PATH` before you spend a retry budget finding out.
