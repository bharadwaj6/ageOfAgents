# ADR 004: Pluggable Agent Backend Behind One Interface

## Status
Accepted

## Context
The orchestrator must drive *real* coding agents end-to-end, but must also be testable and runnable
offline with no API calls or cost. Business logic should never call a provider SDK directly.

## Decision
All agent execution goes through a single **`agent.Backend`** interface, roughly
`Run(ctx, Task) (Proposal, error)`. The implementations behind it are:

- **`mock`** — deterministic, offline. Produces predictable proposals so the *entire* orchestration loop
  runs inside `go test` with no network and no external services.
- **CLI harnesses** — drive a real agent as a subprocess inside the ticket's isolated git worktree.
  These are preset rows in one table rather than a file each; see [ADR 014](014-cli-backends-as-data.md),
  which narrows *how* a CLI-shaped Backend is expressed without changing this seam.
- **API backends** — `openai` and `anthropic` speak to a provider endpoint directly.

The active backend is chosen in `aoa.toml`. New backends (other CLIs/APIs) implement the same interface.

## Consequences
- Deterministic, hermetic tests of the full loop (eval-first); CI needs no secrets.
- The LLM is a single, swappable seam — no provider lock-in in business logic.
- Tradeoff: the interface must stay narrow and provider-agnostic; richer per-provider features live
  behind the implementation, not in the core.

## Research basis
Provider-abstraction discipline (user coding standards); Anthropic agent-as-subprocess pattern; MCP for
tool access if/when needed.
