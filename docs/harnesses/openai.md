# OpenAI (direct API)

**Status: not verified against the live API.** The code is exercised by hermetic tests only.

For OpenAI's *coding agent*, you probably want [codex](codex.md) — it is verified. This backend talks to
the chat API directly and runs its own small agent loop (`bash` + `finish` tools, max 15 iterations).

```toml
backend = "openai"
```

Needs `OPENAI_API_KEY`.

## Cost

Real counts from the API response's `usage.total_tokens`.

```toml
[pricing]
"gpt-5" = 1.25
```

## Any OpenAI-compatible endpoint

Use a plugin block instead — see [deepseek.md](deepseek.md), which is the same mechanism.
