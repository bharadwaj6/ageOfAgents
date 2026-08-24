# Grok (xAI)

**Status: verified.** This is the backend the loop was first proved end to end on.

```toml
backend = "grok"
```

Needs the `grok` CLI on `$PATH`, logged in at grok.com. **No API key.**

## What aoa runs

```
grok --permission-mode bypassPermissions --output-format json -p "<prompt>"
```

Grok's flags are Claude Code-compatible. `bypassPermissions` is the equivalent of claude's
`acceptEdits`: without it the CLI runs but declines to write files.

**Leader daemon.** Headless grok needs an authorizing leader process. `aoa` checks `grok leader list`
once per run and spawns `grok agent leader` if it isn't reachable. That daemon outlives `aoa` by design.

## Cost

Real counts and a real model id, from `usage.total_tokens` and `modelUsage` in the CLI's JSON envelope:

```toml
[pricing]
"grok-4.6-build" = 2.0
```
