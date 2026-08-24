# ADR 014: CLI Backends are Data, Not Code

## Status
Accepted

## Context
`aoa`'s value proposition is that it drives *whatever coding agent you already pay for*. That made the
shape of `internal/agent` load-bearing, and the shape was wrong: `grok.go` was a line-for-line copy of
`claudecode.go`, differing only in a binary name, an error prefix, a default arg list, and an output
parser. Adding codex, cursor and gemini the same way meant five near-identical `Run` methods, and a
harness aoa had never heard of could not be used at all — `[backends.<name>]` only supported
`type = "openai_compatible"`, so every CLI needed a Go `case` and a release.

Meanwhile the harnesses do not agree on how a prompt is passed. `claude -p <prompt>` takes it as a flag
value; `codex exec <prompt>` takes it as a positional — and for codex `-p` is `--profile`, so copying
claude's pattern would have passed the entire prompt as a config-profile name. `cursor-agent`'s `-p` is
a boolean `--print`, and its prompt is also positional.

## Decision
Make a CLI backend **data**: one `agent.CLI` type, and a preset table describing each harness.

- **One invocation rule: `Args` verbatim, then the prompt appended as the final argv element.** That
  covers both conventions — harnesses that want a flag value put the flag last in `Args` (`"-p"`), and
  harnesses that want a positional simply do not. It is smaller than a `{{prompt}}` template, and it
  makes the no-shell property structural rather than a thing to remember: the prompt is one element of
  a `[]string` handed to `exec.Command`, so backticks, `$(...)` and newlines in a Goal pass through
  literally. A harness that needs the prompt elsewhere is a three-line wrapper script on `$PATH` —
  zero code here.
- **The same type backs `[backends.<name>] type = "cli"`.** One mechanism, two doors: first-class names
  for the harnesses people use today, BYOHarness for everything else. A config block shadows a preset
  of the same name, so a preset whose flags have gone stale can be corrected without a release.
- **One tiered output parser**, not one per harness: a single JSON envelope (claude, grok, cursor,
  gemini), then a JSONL event stream (codex), then prose plus the optional `aoa:usage` fence. A harness
  whose format changes degrades to prose rather than lying.
- **Unknown token counts are reported as zero, never guessed.** cursor's envelope has no usage fields
  and gemini's `stats` block has no published field names, so those backends report 0 rather than a
  plausible invention. Because that silently disables `max_usd_per_goal` and `max_tokens_per_goal`,
  `aoa` warns at startup when a governor is set on a backend that cannot feed it.

## Consequences
- Adding a harness is a table row plus, only if it has its own envelope, a case in the parser. The
  test that covers it is a row in `TestCLIPresetArgv`.
- `claudecode` and `grok` are unchanged: same argv, same `Name()` (it is the `[pricing]` key), and
  their existing parser fixtures pass with nothing but a function rename — which is the compat proof.
- `requireCLI` must run **before** a preset's preflight hook. Grok's spawns a detached daemon that
  outlives the process, and the hermetic suite builds that backend with an empty `PATH`.
- Verification status is per-harness and stated honestly in `docs/harnesses/`: codex is verified end to
  end, cursor's flags are verified but no live run has been done, gemini is unverified.
- This does not change ADR 004: all LLM access still goes through `agent.Backend`. It narrows *how* a
  CLI-shaped Backend is expressed.
