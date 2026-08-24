# DeepSeek (and any OpenAI-compatible API)

There is **no official DeepSeek coding-agent CLI**, so `aoa` has no `deepseek` backend and does not
pretend to. DeepSeek serves an OpenAI-compatible API, and `aoa` already speaks that — this is config
only, no new code.

```toml
backend = "deepseek"

[backends.deepseek]
type        = "openai_compatible"
base_url    = "https://api.deepseek.com/v1/chat/completions"
model       = "deepseek-chat"
api_key_env = "DEEPSEEK_API_KEY"
```

Then `export DEEPSEEK_API_KEY=…`. Verify `base_url` and `model` against DeepSeek's current docs — they
are not checked here.

The same block works for **any** OpenAI-shaped endpoint: OpenRouter, Together, Groq, vLLM, LM Studio,
or a corporate gateway. Change `base_url`, `model` and `api_key_env`.

## Cost

Real counts, read from the API response's `usage.total_tokens`. Price it by the model id you configured:

```toml
[pricing]
deepseek-chat = 0.28
```

## If you want a third-party DeepSeek CLI

Several community CLIs wrap DeepSeek. `aoa` can drive any of them without a code change — that is what
[BYOHarness](byo-cli.md) is for.
