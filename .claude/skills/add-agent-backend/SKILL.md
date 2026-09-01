---
name: add-agent-backend
description: Use when adding or changing an agent backend for aoa — supporting a new coding CLI (claude, codex, cursor, gemini, grok and friends), adding a native API backend, or debugging how a backend's argv and token usage are built. Covers internal/agent/cli.go presets, agent.Backend, and the zero-code aoa.toml path.
---

# Adding an agent backend

All LLM access goes through `agent.Backend` (`Name`, `Run`) — never a provider SDK or CLI from business
logic (ADR 004).

## First: does it need Go at all?

A user can already drive any CLI by describing it in `aoa.toml`, with no code change:

```toml
[backends.mytool]
type = "cli"
bin  = "mytool"
args = ["--print"]
```

Only add a preset when the harness is common enough to ship out of the box (ADR 014).

## A CLI harness is a table row, not a file

In `internal/agent/cli.go`, add one entry to `cliPresets`:

```go
"grok": {bin: "grok", args: defaultGrokArgs, reportsUsage: true, preflight: EnsureGrokLeader},
```

- `reportsUsage` — whether the CLI reports its own token counts. Set it honestly: `aoa doctor` and the
  spend governors tell the user when a backend reports nothing, rather than quietly showing `$0`.
- `preflight` — optional check run before dispatch (login state, a required subcommand).
- **The prompt is appended as the final argv element.** A harness that takes the prompt as a flag *value*
  must therefore put that flag last in `args`.

Then:

1. Add a row to `TestCLIPresetArgv` in `internal/agent/agent_test.go` — that test is the contract for the
   argv a preset produces.
2. Add a page under `docs/harnesses/` and a `nav:` entry in `mkdocs.yml` (see `publish-docs-site`), plus a row
   in the README table. Mark it *verified* only if a real Goal actually went through it to a merge.
3. Touch `parseCLIOutput` **only** if the CLI wraps its output in its own envelope (JSON with a usage
   block, say). Plain stdout needs no case.

## A non-CLI backend

Implement `agent.Backend`, register it in `cmd/aoa/main.go:buildBackendSingle`, and document it in
`docs/harnesses/` and the README table. `openai`/`anthropic` are the existing native HTTP examples; an
OpenAI-compatible endpoint is already covered by the `openai_compatible` type.

## Keep the suite offline

Every hermetic test runs on the `mock` backend, which never networks and must stay deterministic. A new
backend gets tests for its argv and output parsing — not for a live call. Live verification belongs in
`scripts/live_smoke.sh` (`make smoke`), outside `make check` (ADR 009).
