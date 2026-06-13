# Gas Town — Architecture Overview

## What It Is

**Gas Town** (`gt`) is a **multi-agent orchestration system** — a Go CLI that coordinates fleets of AI coding agents (Claude Code, GitHub Copilot, Codex, Gemini) working concurrently on shared codebases. The core problem it solves: agents lose context on restart, have no coordination mechanism, and don't persist work state. Gas Town wraps all of that in a structured, git-backed system.

---

## Core Concepts

| Term | What it is |
|---|---|
| **Town** | Your workspace root (`~/gt/`). Contains all rigs + infrastructure. |
| **Rig** | Per-project container. Wraps a git repo + agents + Dolt database. |
| **Bead** | Atomic work unit stored in Dolt (git-backed MySQL DB). Issues = beads. IDs like `gt-abc12`. |
| **Hook** | A special pinned bead assigned to each agent — their active work queue. |
| **Convoy** | Work-order grouping multiple beads. Your view of "what's in flight". |
| **Formula** | TOML workflow template (4 types: convoy, workflow, expansion, aspect). |
| **Molecule** | A durable instantiated multi-step workflow from a Formula. Survives restarts. |
| **Wisp** | Ephemeral, lightweight beads destroyed after use. |

---

## Agent Roles

**Infrastructure roles** (persistent singletons):

- **Mayor** — Your primary coordinator. Entry point for humans. Orchestrates convoys and agents across the whole town.
- **Deacon** — Background daemon that runs patrol cycles across all rigs, detects stuck agents, triggers recovery.
- **Witness** — Per-rig lifecycle manager for polecats. Monitors, nudges, recycles.
- **Refinery** — Per-rig Bors-style merge queue. Batches polecat PRs, runs verification gates, merges to main.

**Worker roles**:

- **Polecat** — Ephemeral-session worker with persistent identity. Spawned for tasks, killed on completion, but its CV/history/worktree persists. Witness-managed.
- **Crew** — Long-lived, user-managed worker. Good for exploratory or ongoing work.
- **Dog** — Deacon's infrastructure helpers (e.g., `Boot` checks Deacon health every 5 min).

---

## Architecture

```
~/gt/
├── mayor/          ← Mayor workspace + town config
├── deacon/         ← Deacon daemon
└── <rig>/          ← One per project
    ├── .repo.git/  ← Bare git repo
    ├── witness/    ← Witness session
    ├── refinery/   ← Merge queue processor
    ├── crew/       ← Long-lived human-directed workers
    └── polecats/   ← Ephemeral worker fleet
```

The data layer is **Dolt** (a git-backed MySQL-compatible database), accessed through the `beads` (`bd`) CLI. Every action is attributed — git commits, bead events, everything carries an actor identity.

Agents communicate via **mail** (async), **nudge** (real-time), and **hooks** (work assignment). The **Seance** system lets agents query previous sessions for context.

---

## Three Key Principles

1. **GUPP** (Gas Town Universal Propulsion Principle): *"If there is work on your Hook, you MUST RUN IT."* Agents are autonomous — no waiting for confirmation.
2. **NDI** (Nondeterministic Idempotence): Eventual workflow completion even when individual agents fail, via Witness/Deacon supervision.
3. **ZFC**: tmux session existence is the source of truth for running state — not files.

---

## Tech Stack

- **Language**: Go, built with Cobra CLI + Bubbletea TUI
- **Storage**: Dolt (git-backed relational DB) + git worktrees for isolation
- **Agent runtimes**: Claude Code (default), Codex, GitHub Copilot CLI, Gemini
- **Observability**: OpenTelemetry (metrics + logs via OTLP)
- **Plugins**: Shell-script-based sidecar binaries in `plugins/` (e.g., `dolt-backup`, `github-sheriff`, `stuck-agent-dog`)
- **Wasteland**: Federated cross-town work network via DoltHub for inter-organization coordination
