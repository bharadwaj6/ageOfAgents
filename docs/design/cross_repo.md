# Cross-Repository Orchestration Design (Drill-Down)

This document outlines the high-level architecture for expanding Age of Agents (`aoa`) from a single-repository orchestrator to a cross-repository workspace manager.

## The Problem
Modern distributed architectures rarely fit into a single repository. A single objective (e.g., "Add a new `discount_code` field to the Checkout API") often requires coordinated changes across:
- The **Context/Architecture Repo** (updating the OpenAPI spec or protobuf definitions).
- The **Backend Repo** (implementing the API and database migrations).
- The **Frontend Repo** (consuming the new field in the UI).

Currently, `aoa` clones a single `repo` specified in `aoa.toml` into an isolated `git worktree` and runs the verification `Gate`.

## The Combined Worktree Solution
To support cross-repo edits without losing the core "single source of truth" and "isolated sandbox" tenets, `aoa` will adopt a **Combined Worktree** model.

### 1. Configuration Changes
`aoa.toml` will evolve to support a list of repositories, potentially mapping them to specific subdirectories inside the workspace:

```toml
[workspace]
name = "ecommerce-platform"
repositories = [
  { name = "arch", url = "git@github.com:myorg/arch.git", path = "./arch" },
  { name = "backend", url = "git@github.com:myorg/backend.git", path = "./backend" },
  { name = "frontend", url = "git@github.com:myorg/frontend.git", path = "./frontend" }
]

verify = [
  ["make", "-C", "arch", "lint"],
  ["make", "-C", "backend", "test"],
  ["npm", "--prefix", "frontend", "test"]
]
```

### 2. Sandbox Creation
The `internal/worktree` package will change from creating `git worktree add` instances of a single repository to:
1. Creating an overarching directory for the task (`.aoa/worktrees/task-123/`).
2. Inside that directory, running `git worktree add` for *each* configured repository into its designated `path`.
3. The LLM agent receives this combined overarching directory as its working directory.

### 3. Verification & The Gate
The `Verifier` will run from the root of the overarching directory. Since all repositories are present, integration tests or cross-repo contract validations (e.g., verifying the backend correctly implements the `arch` OpenAPI spec) can run natively.

### 4. Atomic Multi-Repo Merges
Git does not support atomic commits across repositories natively unless using submodules or monorepos. To solve this in the Merge Queue (`internal/mergequeue`):

**Phase 1: Verification**
The orchestrator creates the combined state, applies the agent's diffs to each respective repository, and runs the `verify` gate. If it fails, all changes are discarded.

**Phase 2: Grouped Pull Requests**
If it passes, `aoa` will push the branches for each modified repository to their respective remotes (e.g., `aoa/task-123`). 

Since the `main` branches of individual repos cannot be merged exactly atomically, `aoa` will:
1. Fast-forward merge each repository locally.
2. Push all fast-forwarded `main` branches sequentially. 
*Note:* If a push fails due to a concurrent human merge on one repo but not the other, `aoa` will encounter a partial merge failure. This requires a retry loop where `aoa` pulls the latest `main` for all repos, reapplies the diffs, and re-verifies.

Alternatively, `aoa` can be configured to simply **open linked Pull Requests** across the repositories and let the native GitHub Merge Queue (or similar CI) handle the final merge coordination.

## Future Exploration
- How does the `prompt` context sizing scale when parsing three codebases? We will need robust RAG or semantic search to ensure the LLM isn't overwhelmed.
- Should we use `git submodules` as the underlying mechanism for the workspace to enforce atomic cross-repo state tracking natively?
