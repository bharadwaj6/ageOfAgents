# Claude Code

**Status: verified.**

```toml
backend = "claudecode"
```

Needs the `claude` CLI on `$PATH` and authenticated.

## What aoa runs

```
claude --permission-mode acceptEdits --output-format json -p "<prompt>"
```

- **`--permission-mode acceptEdits`** is required. Without a permission mode, headless `claude -p` runs
  but declines to write files, so every Task fails with "agent produced no changes". The worktree is
  the agent's sandbox; the Gate, not the agent, decides what merges.
- **`--output-format json`** is what makes cost accounting real here. Without it stdout is prose and
  the only fallback is asking the model to self-report, which produces a confident invented number.

## Cost

Real counts from the CLI's own envelope, summing `input_tokens`, `output_tokens`,
`cache_creation_input_tokens` and `cache_read_input_tokens` — cache reads are real spend. Costs land
under the model id the CLI reports:

```toml
[pricing]
"claude-sonnet-4-5" = 3.0
```
