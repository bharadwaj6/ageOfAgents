# Codex (OpenAI)

**Status: verified end to end.** A Goal was run through this backend on 2026-08-24 with
`codex-cli 0.139.0`, produced a proposal, passed the Gate and merged — 590,623 tokens across 2
attempts, 141.7s wall.

```toml
backend = "codex"
```

Needs the `codex` CLI on `$PATH` and authenticated (`codex login`). `aoa doctor` checks the first.

## What aoa runs

```
codex exec --json --sandbox workspace-write "<prompt>"
```

Two things here are easy to get wrong, and both fail quietly:

- **`-p` is `--profile`, not the prompt.** Unlike `claude -p "<prompt>"`, codex takes the prompt as a
  *positional*. Passing it after `-p` would hand your entire prompt to codex as a config-profile name.
- **`codex exec` defaults to a read-only sandbox.** Without `--sandbox workspace-write` the agent runs,
  changes nothing, and every Task fails with "agent produced no changes".

`--json` gives the JSONL event stream that `aoa` reads token counts from. `-C/--cd` is deliberately not
passed: `aoa` already runs the CLI with the worktree as its working directory.

## Cost

Real counts, from the `turn.completed` event's `usage` block. `aoa` sums `input_tokens + output_tokens`
only — `cached_input_tokens` is a subset of input and `reasoning_output_tokens` a subset of output, so
adding all four would double-count.

The stream carries no model id, so costs land under the key `codex`:

```toml
[pricing]
codex = 10.0   # USD per million tokens
```

## Sandboxing

`--sandbox workspace-write` is codex's own sandbox, and it is not the same thing as `aoa`'s `sandbox`
setting — that one isolates the **Gate**, not the agent. See [`SECURITY.md`](../../SECURITY.md).
