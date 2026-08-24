# Anthropic (direct API)

**Status: not verified against the live API.** The code is exercised by hermetic tests only.

For Anthropic's *coding agent*, you probably want [claude-code](claude-code.md) — it is verified. This
backend talks to the Messages API directly and runs its own small agent loop (`bash` + `finish` tools,
max 15 iterations).

```toml
backend = "anthropic"
```

Needs `ANTHROPIC_API_KEY`.

## Cost

Real counts, summing `input_tokens` and `output_tokens` from the API response.

```toml
[pricing]
"claude-sonnet-4-5" = 3.0
```
