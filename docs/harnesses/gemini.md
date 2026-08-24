# Gemini (Google)

**Status: unverified.** The `gemini` CLI is not installed on the machine this preset was written on.
The flags come from the vendor's published reference, not from a live run. Treat this page as a
starting point and please report what actually happens.

```toml
backend = "gemini"
```

## What aoa runs

```
gemini --approval-mode yolo --output-format json -p "<prompt>"
```

- **`--approval-mode yolo`** lets the agent act without prompting. `-y/--yolo` is deprecated upstream in
  favour of this. Use `auto_edit` instead if you want file writes but not command execution.
- **`-p/--prompt`** takes the prompt as a value, so it is last in the arg list.

## Cost

**Not parsed.** The JSON envelope puts the agent's prose in `response`, which `aoa` reads. Its `stats`
block is documented only as "token usage and API latency metrics" with no published field names, so
`aoa` reports **0 tokens** rather than guess at them — a fabricated cost is worse than a missing one.

If you run this backend, paste the real `stats` block into an issue and it becomes a two-line fix plus
a test fixture.

## If the flags are wrong

Override the preset without waiting for a release:

```toml
backend = "gemini"

[backends.gemini]
type = "cli"
bin  = "gemini"
args = ["--approval-mode", "auto_edit", "--output-format", "json", "-p"]
```
