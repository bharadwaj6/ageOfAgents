# Getting Started with Age of Agents (`aoa`)

Welcome to **Age of Agents**, a minimal, verifier-gated orchestrator for fleets of AI coding agents. This guide will walk you through setting up your first workspace and executing a goal autonomously!

## What is Age of Agents?

Age of Agents is designed to manage and orchestrate AI agents working on your code. Unlike other complex frameworks, it relies on simple, tested distributed-systems primitives:
- **Event Ledger**: An append-only log that acts as the single source of truth.
- **Isolated Workers**: Agents work in isolated git worktrees, preventing conflicts.
- **Merge Queue**: A serialized queue that uses your actual build/test commands as an objective gate. Code only reaches the main branch if it passes your tests.

## 1. Installation

First, clone the repository and build the CLI binary:
```bash
git clone https://github.com/bharadwaj6/ageOfAgents.git
cd ageOfAgents
go build -o aoa ./cmd/aoa
```

## 2. Scaffolding a Workspace

A **workspace** contains the orchestrator configuration, the event ledger, and the git repository your agents will be working on. 

To initialize a new workspace and an integration repo (e.g., a minimal Go module):
```bash
./aoa init --path ./workspace --repo ./demo
```
This command creates:
- A `.aoa/` directory holding the event ledger and worktrees.
- An `aoa.toml` configuration file.
- A `CONVENTIONS.md` file for providing prompt instructions to the agents.
- The `demo/` folder acting as your target Git repository.

## 3. Submitting a Goal

You interact with the orchestrator by submitting high-level goals. The framework will automatically decompose the goal and dispatch tickets to agents.

Submit your first goal:
```bash
./aoa goal --path ./workspace "Add a greeting function to the main package"
```
You can view the goal and pending tickets at any time:
```bash
./aoa status --path ./workspace
```

## 4. Running the Orchestrator

The orchestrator reads the event ledger, detects the pending goal, and spins up agents to execute it. 

To start the orchestrator loop:
```bash
./aoa run --path ./workspace
```
*Note: By default, the `aoa.toml` is configured to use an offline `"mock"` backend, which will instantly simulate work. This is great for testing the workflow!*

### Using a Real AI Agent
To unleash a real coding agent, edit the `aoa.toml` inside your workspace root:
```toml
# Change the backend from "mock" to "claudecode"
backend = "claudecode"
```
Once updated, run `./aoa run --path ./workspace` again. The orchestrator will invoke the agent, process the code, and merge it if it passes verification.

## 5. Monitoring and Auditing

Because everything is driven by the event ledger, you can track exactly what the agents are doing.

To view a real-time feed of events:
```bash
./aoa feed --path ./workspace
```

To view the raw event log:
```bash
./aoa events --path ./workspace tail
```

## 6. Configuring the Verification Gate

The orchestrator enforces an objective gate on the merge queue. Agents' code is only merged if the configured commands exit successfully. You can customize this in your `aoa.toml`:
```toml
verify = [
  ["go", "build", "./..."],
  ["go", "test", "./..."],
  ["golangci-lint", "run"]
]
```

## Next Steps

Now that you have your first workspace running, check out the [Architecture Design Document](architecture.md) for a deeper understanding of the internal deterministic orchestrator and stochastic execution!
