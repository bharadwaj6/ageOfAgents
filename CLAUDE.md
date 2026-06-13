# CLAUDE.md

Project guidance for Claude Code. The full guide is in [`AGENTS.md`](AGENTS.md) — read it.

## Essentials

- **Design first:** read [`docs/design/architecture.md`](docs/design/architecture.md) and the ADRs in
  [`docs/design/adr/`](docs/design/adr/) before structural changes; add an ADR when you make a decision.
- **Non-negotiables:** the Event Log is the single source of truth (all state is derived by replay);
  nothing merges unless the Gate passes; one deterministic Scheduler (no role hierarchy, no LLM
  coordinator); all LLM access goes through `agent.Backend`; no markets/voting/consensus; coordinate via
  the Shared Log, not messaging. (See ADRs 001–006.)
- **Before any commit:** `go build ./... && go vet ./... && go test ./...` green and `gofmt -l` clean.
  The test suite must stay hermetic/offline — the `mock` Backend never makes network calls.
- **Commits:** Conventional Commits, ≤72-char subject, no AI model names in subject/body.

See [`AGENTS.md`](AGENTS.md) for the repository map, glossary, conventions, and common tasks.
