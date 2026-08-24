# Cursor

**Status: flags verified, no live run.** Every flag below was read off `cursor-agent --help`
(2026.05.01-eea359f), but no end-to-end Goal has been run through it: the machine this was written on
has the binary without a logged-in account, and `cursor-agent` exits with "Authentication required"
before it reaches the prompt. If you run it successfully, please say so in an issue.

```toml
backend = "cursor"
```

Needs `cursor-agent` on `$PATH` and authenticated (`cursor-agent login`, or `CURSOR_API_KEY`).

## What aoa runs

```
cursor-agent -p --force --trust --output-format json "<prompt>"
```

- **`-p` is `--print`, a boolean** — it takes no value. The prompt is a positional. `cursor-agent -p
  "<prompt>"` appears to work only because the prompt lands as the positional argument.
- **`--force`** allows commands that are not explicitly denied (`--yolo` is an alias).
- **`--trust`** accepts the workspace without prompting. `aoa` hands the agent a git worktree it has
  never seen, which cursor would otherwise stop to ask about — and a headless run cannot answer.
- **`-w/--worktree` is deliberately not passed.** Cursor would create its *own* worktree under
  `~/.cursor/worktrees/` and `aoa` would find no changes in the one it made.

## Cost

**None reported.** Cursor's JSON envelope (`{type, subtype, is_error, duration_ms, result, session_id,
…}`) carries no token or cost fields at all, so `max_usd_per_goal` and `max_tokens_per_goal` cannot be
enforced on this backend. `aoa` warns at startup if you set one.

To opt in, have the agent emit an [`aoa:usage` fence](README.md#cost-accounting) — self-reported, so
only as trustworthy as the model.
