Here are the sources I drew on, grouped by type. The research process surfaced more than I ended up citing, but these are the ones that actually shaped the report.

**Core empirical papers (multi-agent failure & ensembles)**

- Cemri et al., "Why Do Multi-Agent LLM Systems Fail?" (UC Berkeley Sky Computing Lab, NeurIPS 2025) — the MAST taxonomy, 1,642 traces, the 41–86.7% failure-rate finding: https://arxiv.org/abs/2503.13657 (HTML: https://arxiv.org/html/2503.13657v3 · PDF: https://arxiv.org/pdf/2503.13657)
- Vallecillos-Ruiz, Hort & Moonen, "Wisdom and Delusion of LLM Ensembles for Code Generation and Repair" — the "popularity trap" of consensus voting vs. diversity-based selection: https://arxiv.org/abs/2510.21513 (HTML: https://arxiv.org/html/2510.21513)
- Huang et al., "Large Language Models Cannot Self-Correct Reasoning Yet" (ICLR 2024) — multi-agent debate ≈ self-consistency at equal cost: https://arxiv.org/pdf/2310.01798
- "Shapley-Coop: Credit Assignment for Emergent Cooperation in Self-Interested LLM Agents" — game-theoretic credit assignment for *self-interested* agents: https://arxiv.org/pdf/2506.07388

**System / engineering sources**

- Anthropic, "How we built our multi-agent research system" — orchestrator-worker pattern, the 90.2% figure, ~15× token cost, the warning about interdependent tasks: https://www.anthropic.com/engineering/multi-agent-research-system
- Anthropic, "Building Effective AI Agents: Architecture Patterns and Implementation Frameworks" (PDF): https://resources.anthropic.com/hubfs/Building%20Effective%20AI%20Agents-%20Architecture%20Patterns%20and%20Implementation%20Frameworks.pdf
- Secondary coverage of the Anthropic blueprint (production caveats): https://fountaincity.tech/resources/blog/anthropic-multi-agent-blueprint-production/ and https://blog.bytebytego.com/p/how-anthropic-built-a-multi-agent

**Distributed-systems primitives (reference material)**

- Actor model / supervision trees background: https://berb.github.io/diploma-thesis/original/054_actors.html
- SAGA pattern (Richardson, microservices.io): https://microservices.io/patterns/data/saga.html

**Framework landscape**

- "2026 AI Agent Framework Showdown: LangGraph vs CrewAI vs AG2 vs Claude SDK vs Strands vs OpenAI": https://qubittool.com/blog/ai-agent-framework-comparison-2026

A few things worth flagging on provenance. Several canonical works I referenced — Raft (Ongaro & Ousterhout), Contract Net Protocol (Smith 1980), STEAM (Tambe), Ant Colony Optimization (Dorigo), the Hearsay-II blackboard architecture, CRDTs (Shapiro et al.), phi-accrual failure detectors (Hayashibara et al.), and the edge-of-chaos / self-organized criticality literature (Langton, Bak, Mitchell) — are drawn from established knowledge rather than fetched in this session, so I don't have a specific URL I pulled them from. If you want primary links for any of those, I can grab the canonical paper for each. The Gastown internals came from its own README/docs at https://github.com/gastownhall/gastown.

One caveat: the QubitTool and the two secondary Anthropic-blueprint posts are lower-tier sources (aggregators/blogs) — fine for landscape orientation, but for anything load-bearing I'd lean on the arXiv papers and Anthropic's own engineering post. Want me to pull primary sources for the classical distributed-systems and MAS references too?

---

**Here is a compiled list of the key resources, research papers, websites, GitHub repositories, and other materials referenced or used throughout this conversation.**

I’ve organized them by topic for easier navigation.

### 1. Gas Town & Multi-Agent Orchestration
- **Main Repository**: https://github.com/gastownhall/gastown  
  (Core source for architecture, concepts, molecules, hooks, glossary, and design docs)
- **README and Key Docs** (via GitHub):
  - https://github.com/gastownhall/gastown/blob/main/README.md
  - https://github.com/gastownhall/gastown/blob/main/docs/design/architecture.md
  - https://github.com/gastownhall/gastown/blob/main/docs/concepts/molecules.md
  - https://github.com/gastownhall/gastown/blob/main/docs/glossary.md
  - https://github.com/gastownhall/gastown/blob/main/docs/HOOKS.md
- **Related Articles & Discussions**:
  - Steve Yegge’s “Welcome to Gas Town” (Medium)
  - Various analyses on Shinzo.ai, Better Stack, and HN threads about Gas Town vs. other orchestrators (e.g., Conductor)
  - Gas Town Hall org: https://github.com/gastownhall

### 2. Distributed Systems, Game Theory & Multi-Agent Coordination
- kirancodes.me post: “Multi-agentic Software Development is a Distributed Systems Problem” (https://kirancodes.me/posts/log-distributed-llms.html) and associated HN discussion
- arXiv papers from searches, including:
  - “Coordination as an Architectural Layer for LLM-Based Multi-Agent Systems” (arXiv:2605.03310)
  - “AgentNet: Decentralized Evolutionary Coordination for LLM-based Multi-Agent Systems” (NeurIPS)
  - Game-theoretic surveys of LLM-based MAS
- General references on blackboard architectures, stigmergy, and swarm patterns in multi-agent systems

### 3. Slime Mold Algorithms (Physarum polycephalum)
- **Foundational Papers**:
  - Tero et al. (2010). “Rules for Biologically Inspired Adaptive Network Design.” *Science* 327(5964): 439–442. (https://www.science.org/doi/10.1126/science.1177894)
  - Tero et al. (2007). Mathematical model for adaptive transport network (Journal of Theoretical Biology)
- **Slime Mould Algorithm (SMA)**:
  - Li et al. (2020). “Slime mould algorithm: A new method for stochastic optimization.” *Future Generation Computer Systems*
  - Wikiversity page: https://en.wikiversity.org/wiki/Slime_Mould_Algorithm
  - Comprehensive surveys (e.g., Gharehchopogh et al. 2023 on SMA)
- **Multi-Agent Computational Models**:
  - Jeff Jones papers, especially arXiv:1511.05774 (“Applications of Multi-Agent Slime Mould Computing”)
  - Book: *From Pattern Formation to Material Computation: Multi-agent Modelling of Physarum Polycephalum* by Jeff Jones (2015)
- GitHub implementations for SMA (various repos, e.g., aliasgharheidaricom)

### 4. Ant Colony Optimization (ACO)
- **Wikipedia (excellent overview)**: https://en.wikipedia.org/wiki/Ant_colony_optimization_algorithms
- **Core Book**: Dorigo, M. & Stützle, T. *Ant Colony Optimization* (MIT Press)
- **Key Original Papers**:
  - Dorigo (1992) – Ant System (PhD thesis / early papers)
  - Dorigo & Gambardella (1997) – Ant Colony System (ACS)
  - Stützle & Hoos (2000) – MAX-MIN Ant System (MMAS)
- **Comparative Studies**:
  - arXiv:2405.15397 – “Comparative Analysis of Four Prominent Ant Colony Optimization Variants: Ant System, Rank-Based Ant System, Max-Min Ant System, and Ant Colony System”
  - Various 2017–2024 papers comparing AS, ACS, and MMAS on TSP and other problems
- **Tutorials & Pseudo-code**:
  - GeeksforGeeks ACO introduction
  - Multiple GitHub implementations of ACO (search “Ant Colony Optimization Python” or “ACO TSP”)

### 5. MAX-MIN Ant System (MMAS) & Variants
- Original MMAS paper: Stützle, T. & Hoos, H.H. (2000). “MAX–MIN Ant System.” *Future Generation Computer Systems*
- Extensions and variants discussed in surveys and comparative papers (including hybrids with local search, parallel versions, bound adaptations, reinitialization strategies, and combinations with other metaheuristics)
- Recent improved MMAS papers (chaotic, opposition-based, elitist, etc.) appear in optimization journals

### 6. General / Cross-Cutting Resources
- arXiv.org (multiple papers on multi-agent LLM coordination, decentralized systems, game theory in MAS, and bio-inspired algorithms)
- Nature, Science, and IEEE papers referenced in the threads
- GitHub repositories for implementations of SMA and ACO algorithms

Would you like me to expand any section with direct DOIs, more specific paper titles, or organize them into a downloadable reference list format (e.g., BibTeX-style or Markdown with annotations)? I can also pull the most important direct PDF links where publicly available.

---


Here are the resources, papers, and repositories that informed the architectural analysis and theoretical frameworks in the report:

**Gas Town Architecture & Analysis**

* **Gas Town GitHub Repository:** [https://github.com/gastownhall/gastown](https://github.com/gastownhall/gastown) 


* **"Welcome to Gas Town"** by Steve Yegge (Detailed breakdown of the design philosophy, "cattle vs. pets" metaphor, and the Gas Town Universal Propulsion Principle): [https://steve-yegge.medium.com/welcome-to-gas-town-4f25ee16dd04](https://steve-yegge.medium.com/welcome-to-gas-town-4f25ee16dd04) 


* **BetterStack Guide to Gas Town** (Operational breakdown of Rigs, The Mayor, Polecats, and Beads): [https://betterstack.com/community/guides/ai/gas-town-multi-agent/](https://betterstack.com/community/guides/ai/gas-town-multi-agent/) 


* **"Gastown: Two Kinds of Multi-Agent"** by Paddo (Analysis comparing Gas Town's system operations metaphor to the older "cargo cult" human persona simulations): [https://paddo.dev/blog/gastown-two-kinds-of-multi-agent/](https://paddo.dev/blog/gastown-two-kinds-of-multi-agent/) 



**Distributed Systems & Memory Scaling**

* **"Language Model Teams as Distributed Systems"** (Highlights Amdahl's Law applied to agents and the structural vulnerabilities of centralized topologies): [https://arxiv.org/abs/2603.12229](https://arxiv.org/abs/2603.12229) 


* **"Multi-Agent Memory from a Computer Architecture Perspective"** (Proposes the distributed three-tier cache/memory hierarchy to prevent context coupling): [https://arxiv.org/abs/2603.10062](https://arxiv.org/abs/2603.10062) 


* **AgentsNet Benchmark** (Research covering dynamic leader election, network diameter boundaries, and peer-to-peer topologies): [https://arxiv.org/pdf/2507.08616](https://arxiv.org/pdf/2507.08616) 



**Stochastic Consensus & State Resolution**

* **Aegean Protocol Framework** (Explains why Raft/Paxos fail for AI, and introduces stability horizons to filter stochastic hallucinations): [https://arxiv.org/html/2512.20184](https://arxiv.org/html/2512.20184) 


* **"Council Mode: Mitigating Hallucination and Bias in LLMs via Multi-Agent Consensus"** (Outlines the three-phase synthesis pipeline for resolving cognitive conflicts): [https://arxiv.org/abs/2604.02923](https://arxiv.org/abs/2604.02923) 


* **"Multi-Agent Consensus Mechanisms: A Complete Technical Comparison"** (Contrasts classical fault tolerance with AI-native cognitive fault tolerance): [https://dev.to/chunxiaoxx/multi-agent-consensus-mechanisms-a-complete-technical-comparison-b8h](https://dev.to/chunxiaoxx/multi-agent-consensus-mechanisms-a-complete-technical-comparison-b8h) 



**Mechanism Design & Complex Adaptive Systems**

* **"Game-Theoretic Lens on LLM-based Multi-Agent Systems"** (Details incentive-compatible communication and Nash equilibrium in decentralized coordination): [https://arxiv.org/abs/2601.15047](https://arxiv.org/abs/2601.15047) 


* **Market-Based Task Allocation** (Research outlining the Contract Net Protocol, spreading bids, and dynamic hierarchical auctions): [https://arxiv.org/html/2508.12683](https://arxiv.org/html/2508.12683) 


* **"Improving the Efficiency of Language Agent Teams with Adaptive Task Graphs"** (Introduces the LATTE framework for fluid, emergent task decomposition over static planning): [https://arxiv.org/pdf/2605.06320](https://arxiv.org/pdf/2605.06320) 


* **Agent Coordination Layer Concepts** (Discusses using operational ontologies to pass highly structured, compact state objects rather than full contexts): [https://www.emergentmind.com/topics/agent-coordination-layer](https://www.emergentmind.com/topics/agent-coordination-layer) 


* **AI Agentic Ecosystems & Complex Adaptive Systems** (Examines how multi-agent networks operate via synergetics and fluid role assignment to absorb complex computational shocks): [https://medium.com/@oracle_43885/ai-agentic-ecosystems-adaptive-governance-with-dupont-accounting-1d3b6ed7f64f](https://medium.com/@oracle_43885/ai-agentic-ecosystems-adaptive-governance-with-dupont-accounting-1d3b6ed7f64f)


Most of the analysis I gave was synthesized from established distributed systems, multi-agent systems, and game theory concepts rather than being a summary of a single paper. If you want to seriously pursue this line of research, I’d start with these resources.

Gas Town / Beads

* Gas Town GitHub Repository⁠￼
* Beads GitHub Repository⁠￼
* Gas City GitHub Repository⁠￼

Steve Yegge’s writings:

* Welcome to the Wasteland: A Thousand Gas Towns⁠￼
* Gas Town: From Clown Show to v1.0⁠￼

⸻

Distributed Systems (Most Relevant)

If your intuition is “agents are nodes, not employees”, this is the foundation.

Consensus

* Paxos Made Simple (Leslie Lamport)⁠￼
* In Search of an Understandable Consensus Algorithm (Raft Paper)⁠￼
* Raft Visualization⁠￼

Distributed Systems Book

* Designing Data-Intensive Applications (Martin Kleppmann)⁠￼

This is probably the single highest-value book for your idea.

⸻

CRDTs and Eventually Consistent Coordination

A lot of agent frameworks assume coordination is necessary.

CRDT research asks:

What if coordination can be avoided?

* CRDTs Explained (Martin Kleppmann)⁠￼
* A Comprehensive Study of CRDTs⁠￼

This area feels massively underapplied to agent systems.

⸻

Actor Model

Agents resemble actors more than employees.

* A Universal Modular Actor Formalism (Carl Hewitt)⁠￼
* Akka Documentation⁠￼
* Orleans Virtual Actors⁠￼

⸻

Complex Adaptive Systems

This is where the “slime mold” intuition comes from.

* Complex Adaptive Systems Institute (Santa Fe Institute)⁠￼
* Thinking in Systems (Donella Meadows)⁠￼

⸻

Swarm Intelligence

If you want alternatives to hierarchy:

* Swarm Intelligence Book (Bonabeau et al.)⁠￼
* Ant Colony Optimization Overview⁠￼

Key concept: stigmergy.

Agents coordinate through shared environment state rather than direct communication.

That’s probably closer to your thinking than org charts.

⸻

Multi-Agent Systems (Academic Field)

This is the discipline that actually studies agent coordination.

* International Foundation for Autonomous Agents and Multiagent Systems (AAMAS)⁠￼
* Multiagent Systems (Shoham & Leyton-Brown)⁠￼

This book is basically “distributed systems meets game theory.”

⸻

Mechanism Design and Game Theory

If agents are unreliable, incentives matter.

* Algorithmic Game Theory Book⁠￼
* Game Theory 101 (Yale Open Course)⁠￼

Particularly relevant:

* reputation systems
* voting systems
* incentive alignment
* mechanism design

⸻

Byzantine Fault Tolerance

This is the biggest parallel with LLM agents.

Distributed systems assume nodes can lie.

LLMs hallucinate.

The analogy is surprisingly strong.

* Byzantine Generals Problem (Lamport)⁠￼
* Practical Byzantine Fault Tolerance (PBFT)⁠￼

⸻

Kubernetes and Control Theory

I suspect this is where your future architecture will eventually land.

Study:

* Kubernetes Controllers Documentation⁠￼
* Operator Pattern Documentation⁠￼

The reconciliation loop:

Desired State
      ↓
Current State
      ↓
Difference
      ↓
Action
      ↓
Repeat

is arguably a better primitive for agent coordination than manager-worker hierarchies.

⸻

If I Were Building a Research Roadmap

I would read in this order:

1. Designing Data-Intensive Applications
2. Raft
3. CRDTs
4. Multiagent Systems (Shoham & Leyton-Brown)
5. Byzantine Fault Tolerance
6. Thinking in Systems
7. Swarm Intelligence
8. Kubernetes Controller Pattern

The research question I’d pursue is:

Can agent coordination be modeled as a distributed state convergence problem rather than a task delegation problem?

That single question connects distributed systems, multi-agent systems, game theory, swarm intelligence, and agentic AI. It also directly challenges the underlying assumption behind frameworks like Gas Town.
