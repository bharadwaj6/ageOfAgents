# Harnesses

`aoa` does not ship an agent. It drives the coding agent you already pay for, in an isolated git
worktree, and merges the result only if your Gate passes. Point it at whichever of these you have.

Set `backend` in `aoa.toml`:

```toml
backend = "codex"
```

| `backend` | Needs | Reports tokens/cost? | Status |
|---|---|---|---|
| [`mock`](#mock) | nothing | n/a | the offline fixture every hermetic test runs on |
| [`claudecode`](claude-code.md) | the `claude` CLI, authenticated | ✅ real counts | verified |
| [`codex`](codex.md) | the `codex` CLI, authenticated | ✅ real counts | **verified end to end** |
| [`cursor`](cursor.md) | the `cursor-agent` CLI, authenticated | ❌ none reported | flags verified; no live run |
| [`grok`](grok.md) | the `grok` CLI (grok.com login, no API key) | ✅ real counts | verified |
| [`gemini`](gemini.md) | the `gemini` CLI, authenticated | ❌ not yet parsed | **unverified** |
| [`openai`](openai.md) | `OPENAI_API_KEY` | ✅ real counts | not verified against the live API |
| [`anthropic`](anthropic.md) | `ANTHROPIC_API_KEY` | ✅ real counts | not verified against the live API |
| [`deepseek` and other OpenAI-compatible APIs](deepseek.md) | an API key | ✅ real counts | config only, no new code |
| [any other CLI](byo-cli.md) | the CLI | ❌ unless it emits an `aoa:usage` fence | BYOHarness |

**"Status" is not marketing.** *Verified* means a real Goal was run through that backend end to end on
this project and merged through the Gate. *Unverified* means the flags come from the vendor's published
reference and nobody has run it. If a preset's flags are wrong for your version, you do not have to wait
for a release — a `[backends.<name>]` block **shadows a built-in of the same name**:

```toml
backend = "codex"

[backends.codex]          # overrides the built-in preset
type = "cli"
bin  = "codex"
args = ["exec", "--json", "--sandbox", "workspace-write", "--some-new-flag"]
```

## Check before you run

```bash
aoa doctor --path ./workspace
```

`doctor` tells you whether the configured backend's CLI is on `$PATH`, whether every Gate command
resolves, and whether the spend governors can actually work on that backend. It does **not** check that
you are logged in — every one of these CLIs would need a billable call to prove that.

## What a backend has to do

A backend gets a prompt and a git worktree, and is expected to edit files in place. That is the whole
contract. It does not need to commit, push, or open anything: `aoa` snapshots whatever changed in the
worktree, runs your Gate on the post-merge state, and keeps it only if the Gate passes.

The prompt is always passed as a **single argv element**, never through a shell, so a Goal containing
backticks or `$(...)` is passed through literally rather than executed.

## Cost accounting

The spend governors (`max_tokens_per_goal`, `max_usd_per_goal`) are driven by token counts the backend
reports. A backend that reports none leaves them inert — `aoa` says so at startup rather than silently
reporting `$0`. Any backend, including a BYO one, can opt in by having the agent emit a fence:

````
```aoa:usage
{"tokens": 4321, "model": "mycoder-1"}
```
````

That number is self-reported, so it is only as trustworthy as the model. Backends marked ✅ above read
their counts from the CLI's own JSON output instead, which is why they are preferred for real spend.

## mock

The default. `backend = "mock"` is a **fixture, not a tiny model**: it writes one placeholder file named
after the Task. It exists so the whole loop — worktree, Gate, serialized merge, Event Log — runs offline
with no key and no cost, which is what `aoa quickstart` demonstrates.
