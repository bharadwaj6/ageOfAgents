# CLAUDE.md

Project guidance for Claude Code. The full guide is in [`AGENTS.md`](AGENTS.md) — read it.

## Essentials

- **Design first:** read [`docs/v2/architecture.md`](docs/v2/architecture.md) and the ADRs in
  [`docs/v2/adr/`](docs/v2/adr/) before structural changes; add an ADR when you make a decision.
- **Non-negotiables:** event log is the single source of truth (all state is a fold); nothing merges
  unless the objective verifier passes; one deterministic reconciler (no role hierarchy, no LLM
  coordinator); all LLM access goes through `agent.Backend`; no markets/voting/consensus; coordinate via
  shared state, not messaging. (See ADRs 001–006.)
- **Before any commit:** `go build ./... && go vet ./... && go test ./...` green and `gofmt -l` clean.
  The test suite must stay hermetic/offline — the `mock` backend never makes network calls.
- **Commits:** Conventional Commits, ≤72-char subject, no AI model names in subject/body.

See [`AGENTS.md`](AGENTS.md) for the repository map, conventions, and common tasks.
