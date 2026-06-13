Gas Town is a local-first, git-native multi-agent workspace manager that coordinates ~20–30 coding agents in parallel around a persistent work ledger, not just a bigger “copilot.” It borrows some organizational ideas (coordinator roles, watchdogs, merge queues) but is much closer to a distributed build/orchestration system than to an org-chart simulator, which is exactly where you can push further using distributed-systems and game-theoretic ideas rather than human hierarchy.[1][2][3][4]

Below is a concise breakdown and then some directions for a “rival” architecture closer to what you’re imagining.

***

## What Gas Town actually does

- Gas Town manages a “town” workspace (e.g. `~/gt`) containing multiple “rigs” (projects/repos), and orchestrates AI coding agents (Claude Code, Copilot, Codex, Gemini, etc.) via the `gt` CLI, tmux sessions, and a git-backed ledger called Beads.[5][6][1]
- Instead of driving a single agent interactively, you give a goal to the **Mayor**; Gas Town decomposes it into issues (“beads”), groups them into **convoys**, spawns worker agents to work in isolated git worktrees, supervises them, and runs a merge queue to integrate results.[3][1][5]
- Its defining characteristics in practice: 20–30 agents running simultaneously, clear separation between coordination and execution roles, persistent work state in git/Beads, and real-time visibility via terminal/dashboard.[2][7][1][3]

High-level intent: **treat agent work as structured, attributable data with history and provenance**, so you can answer “who did what, how well, and where are we across many repos?” at scale.[8][9][2]

***

## Core concepts and vocabulary

### Beads, issues, and work as data

- **Beads** are git-backed structured records for issues, tasks, MRs, etc., stored under `.beads/` with IDs like `gt-abc12`, `hq-x7k2m`; prefix encodes scope and origin.[6][10][1][5]
- Gas Town uses a **two-level beads architecture**:  
  - Town-level beads in `~/gt/.beads/` with `hq-*` prefixes for cross-rig coordination, Mayor mail, global role definitions.[10][8]
  - Rig-level beads in `<rig>/mayor/rig/.beads/` with project-specific prefixes for bugs/features, project issues, merge requests, and rig-scoped agents.[8][10]
- Design philosophy: “work is data”; everything is queryable, attributable, and part of a history that underpins routing, performance evaluation, and federation.[9][11][2]

### Role taxonomy (operational, not persona)

Gas Town’s roles are deliberately operational rather than “PM/Dev/QA personas.”[4][2]

Infrastructure roles:[2][8]
- **Mayor** – global coordinator at `mayor/`, singleton, persistent; plans work, coordinates agents across rigs, handles escalations.[3][5][8]
- **Deacon** – background supervisor daemon; receives heartbeats, runs patrols, dispatches helper agents (Dogs) and escalations.[5][8]
- **Witness** – per-rig lifecycle manager; monitors worker agents (Polecats), detects stuck sessions, nudges or recycles them, handles cleanup.[5][8]
- **Refinery** – per-rig merge queue; batches MRs from workers, runs verification, bisects failing stacks, merges green ones (Bors-style).[1][8][5]
- **Dogs** – short-lived Deacon-owned helpers for infrastructure tasks (e.g., Boot dog for Deacon triage), not for user feature work.[8][5]

Worker roles:[8]
- **Polecats** – ephemeral worker agents with their own git worktrees; execute discrete tasks then terminate; overseen by Witness.[4][1][8]
- **Crew** – persistent human workspaces (full clones) for developers for exploratory or long-running work under direct human control.[1][8]

Each agent has an **agent bead** whose schema references a **role bead** (e.g., `hq-mayor-role`, `hq-polecat-role`) that defines its responsibilities and configuration.[10][8]

### Convoys and hooks

- A **convoy** is a batched unit of work: a named group of beads representing what’s currently “in flight,” with cross-rig visibility and history.[5][8]
- The **“swarm”** is just the set of workers currently assigned to the convoy’s issues; when issues close, the convoy “lands” and you have a durable record of what ran.[5][8]
- **Hooks** are git worktrees that act as persistent mailboxes and state for agents; each Polecat/Refinery works in its own hook, so crashes don’t corrupt shared repos and everything is version-controlled.[3][4][1][5]
- The **Propulsion Principle**: “If you find something on your hook, YOU RUN IT.” Hooks are the only assignment signal; agents don’t wait for extra confirmation.[8][5]

### Federation and Wasteland

- **Federation (design spec)**: a git-native, URI-based architecture for multi-workspace coordination; entities form a flat graph (employment, cross-reference, delegation) across workspaces using URIs like `hop://`/`beads://`.[11]
- **Wasteland**: a concrete DoltHub-backed coordination network linking multiple Gas Towns; rigs post work to a shared “wanted board,” claim tasks, submit completion evidence, and earn portable reputation via multi-dimensional “stamps.”[12][5]

***

## Overall architecture

### Local-first workspace orchestration

- Gas Town is implemented as a Go CLI plus daemon that manages a “town” directory (`~/gt`) containing rigs (repos) and all `.beads`, hooks, and config.[1][5]
- It orchestrates external agent CLIs (Claude Code, Copilot, etc.) in tmux panes, with the Mayor as your primary interface (`gt mayor attach`).[1][5]
- It provides a TUI (`gt feed`) and web dashboard (`gt dashboard`) to monitor agents, convoys, hooks, queues, and escalations in real-time.[1]

Architecturally, it behaves like a **local distributed build/orchestration system**:[3][5][1]

- Central coordinator (Mayor) for intent decomposition and routing.  
- Work ledger (Beads) plus convoys as job graph.  
- Worker pool (Polecats, Crew) in isolated worktrees.  
- Supervisors (Witness, Deacon, Dogs) for health, nudges, escalations.  
- Merge queue (Refinery) for deterministic, gate-checked integration.  

### Two-level beads and routing

- Town-level beads (in `~/gt/.beads`) hold global coordination data and identities; rig-level beads (in `<rig>/mayor/rig/.beads`) hold project issues and agent state.[10][8]
- A `routes.jsonl` mapping of issue prefixes to rig locations sends work to the correct project while keeping canonical `.beads` per rig, and the Beads “multi-agent” layer also supports cross-repo dependencies and agent assignment.[6][10][8]

### Watchdog chain, escalation, and merge queue

- Gas Town builds a three-tier watchdog chain: Go daemon → Boot dog → Deacon → Witness/Refinery, with heartbeats and patrols to detect stuck agents and degraded components.[5][1]
- Problems are surfaced via a “problems view” that classifies agents as GUPP violations, stalled, zombie, working, or idle, with commands to nudge or handoff work.[1]
- Escalations are structured beads with severities (CRITICAL/HIGH/MEDIUM) and flow through Deacon → Mayor → Overseer; the Refinery implements a Bors-style merge queue so workers never push directly to `main`.[5][1]

***

## How close is this to “human org structure”?

There are really three layers here:

1. **Org-chart simulators (BMAD, SpecKit)** – long chains of persona agents (analyst → PM → architect → dev → QA) with phase gates; they mimick SDLC and human reporting structures.[4]
2. **Gas Town** – a small fixed set of *operational* roles (Mayor, Deacon, Witness, Refinery, Polecats) coordinated by external state and git, not SDLC personas.[2][4][8]
3. **Distributed-systems–native designs** – leader election, distributed schedulers, gossip, etc., where coordination is emergent and self-healing rather than centrally orchestrated.  

The “two kinds of multi-agent” piece argues explicitly that Gas Town is **not** an org-chart simulator: it uses operational roles + external memory and avoids context-bloating personas and SDLC phase-gates. That’s aligned with your instinct that simulating human structures is a trap.[4]

However, Gas Town still looks a lot like a classic orchestrator + worker-pool system:

- Single logical coordinator (Mayor), single logical supervisor (Deacon), static roles defined upfront as role beads.[2][4][8]
- Local-first state: when the Refinery or Mayor break, users describe “the city losing consensus reality,” which is very different from a replicated, remote consensus store.[7]
- Scaling mostly by adding more worker agents (20–30 in parallel), which has led to serious chaos and high token burn at real adopters (e.g., $100/hour burn, “murderous Deacon”).[7][3][4]

So your critique is fair: **it removes a lot of human-org baggage but still lives squarely in the centralized scheduler / worker-pool paradigm**, not in the leaderless, self-organizing or market-based DS territory you’re thinking of.

***

## Alternative coordination approaches inspired by distributed systems and game theory

Here are concrete directions for a more “agent-native” coordination layer that goes beyond Gas Town’s design while reusing its good ideas.

### 1. Leader-elected or replicated coordinators instead of a fixed Mayor

- Instead of a single configured Mayor and Deacon, treat “coordinator” as a *role* that any of a set of supervisory agents can hold, with leader election and leases over a remote state store (etcd, Postgres, etc.).  
- Coordinators monitor each other; if the current leader’s decisions degrade quality (too many failing merges, slow response, missing heartbeats), others can trigger a re-election.  
- Role beads (`mayor-role`, `deacon-role`) would point to the *current* holder selected by consensus, not a fixed instance.  

This gives you scheduler/overseer **high availability and self-healing**, more like HA control planes in Kubernetes or Mesos than a single Mayor daemon.

### 2. Market-based task routing instead of top-down assignment

Gas Town already has:  

- Agent CVs and work history (who is good at what).[2]
- Wasteland’s notion of effort, priority, and reputation stamps applied to rigs.[12]

You can combine these into a **market for tasks**:

- Each task (bead) publishes required skills, risk, and reward (e.g., “Go backend medium-effort, high priority, high reward”).  
- Agents estimate their success probability based on their CVs and bid on tasks (expected payoff = success_prob × reward − cost).  
- A market mechanism (first-price, second-price, or bespoke) allocates tasks based on bids, penalizing failures and rewarding successful completions.  

This aligns with distributed mechanism design rather than a central scheduler picking agents, and lets specialization *emerge* from agent behavior instead of being encoded in role templates.

### 3. Failure detection and self-healing with DS techniques

Rather than ad-hoc patrol loops:

- Use **accrual failure detectors** on agent heartbeats (or on their commit/PR/CI behavior), keeping suspicion scores in a remote store.  
- If an agent’s suspicion score trips a threshold, auto-quarantine its hooks, reassign its tasks, and reduce its “reputation weight” in the bidding process.  
- Treat agent configurations and weights as *control knobs* in a feedback system driven by telemetry (errors, retries, CI failures, review rejections).  

Gas Town already emits OTEL telemetry for many operations; this is a natural extension.[1]

### 4. Remote-first ground truth: PRs + CI + policy as the hard substrate

Chainguard’s critique of Gas Town is that **local state is fog; remote state is a contract** and that PRs + CI + audit logs should be the actual ground truth. For a new system:[7]

- Define PRs (or change-proposals) as the **unit of work and coordination**; everything, including agent tasks and convoys, reduces to PRs or chains of PRs.  
- Treat CI and policy engines (e.g., OPA, org rules) as **immutable guardrails**—agents can’t override them; they only adapt their behavior in response.  
- The “Refinery” becomes a *remote* PR-queue operator watching CI and merge rules in the remote system, not a local daemon with implicit authority.[7][3]

Now coordination is grounded in the same shared truth humans already rely on, and agents become high-throughput clients of that system instead of a separate local universe.

### 5. Graph-based work representation instead of town/rig hierarchy

Federation already hints at a **flat, relationship-first model**: entities, relationships, and aggregation over a global graph. You can make that the core:[11]

- Represent tasks, agents, repos, and orgs as nodes in a labeled property graph; edges encode depends-on, delegates-to, blocks, validated-by, etc.  
- Use graph algorithms (topological sort, centrality, community detection) to drive scheduling and risk assessment rather than town/rig trees.  
- Agents discover work by traversing this graph (“find high-centrality tasks in my skill area that are currently blocking others”).  

Gas Town’s two-level beads and cross-repo dependencies already provide much of the raw data; you’re changing the *query and decision model*, not the underlying ledger.[6][10][8]

### 6. Explicit multi-agent protocols

Gas Town has implicit protocols (“if there’s work on your hook, run it; if stuck, escalate”), but you could make protocols explicit and analyzable:

- **Contract-Net** protocols for task allocation (call-for-proposals, bids, awards).  
- **Two-phase or three-phase commit–like flows** for coordinated multi-repo changes.  
- **Gossip** protocols to spread knowledge about model failures, novel exploits, or improved strategies among agents.  

These could be enforced on the ledger (state machines over bead transitions) and make it easier to reason formally about safety and liveness.

***

## How to position a “rival” system

If you build an alternative coordination system, you can:

- **Keep** Gas Town’s best ideas: externalized state and provenance, git and PRs as integration points, a small set of operational roles, deterministic merge queues, OTEL telemetry, and federation/federated reputation.[11][12][3][2][1]
- **Drop or soften** the constraints: single Mayor/Deacon, local-first “city,” static role taxonomy, and the assumption that scaling = “throw more agents at it.”[3][4][7]
- **Add** distributed-systems and game-theory primitives: leader election, replicated control planes, market-based task routing, failure detectors, graph-based work modeling, and protocol-level guarantees.  

That would give you a coordination fabric where agents behave less like employees in a company and more like nodes in a self-organizing, fault-tolerant cluster—closer to Kafka + Kubernetes + a market, and further away from human management patterns.

Sources
[1] Gas Town Hall - GitHub https://github.com/gastownhall
[2] Gas Town Architecture https://docs.gastownhall.ai/design/architecture/
[3] Installing Gas Town https://gastown.dev/docs/INSTALLING/
[4] gastown/docs/agent-provider-integration.md at main - GitHub https://github.com/gastownhall/gastown/blob/main/docs/agent-provider-integration.md
[5] Why These Features? | Gas Town Docs https://docs.gastownhall.ai/other/why-these-features/
[6] Gas Town on Microsoft's AI Stack | Code To Cloud https://codetocloud.io/blog/gas-town
[7] Understanding Gas Town https://docs.gastownhall.ai
[8] GasTown and the Two Kinds of Multi-Agent https://paddo.dev/blog/gastown-two-kinds-of-multi-agent/
[9] Multi-Agent | Beads Documentation - GitHub Pages https://gastownhall.github.io/beads/multi-agent
[10] Federation Architecture | Gas Town Docs https://docs.gastownhall.ai/design/federation/
[11] Gt Wl Join Fails With... https://github.com/gastownhall/gastown/blob/main/docs/WASTELAND.md
[12] Gastown, and where software is going - Chainguard https://www.chainguard.dev/unchained/gastown-and-where-software-is-going
[13] gastown/docs/glossary.md at main - GitHub https://github.com/gastownhall/gastown/blob/main/docs/glossary.md
[14] Releases · gastownhall/gastown https://github.com/gastownhall/gastown/releases
[15] The Prius of GasTown - Trilogy AI Center of Excellence https://trilogyai.substack.com/p/the-prius-of-gastown
[16] gastownhall/gastown: Gas Town - multi-agent workspace ... https://github.com/gastownhall/gastown
