# Reading list

The sources the architecture actually rests on. Each entry says **what claim it supports**, so a reader
can check the claim against the source rather than take the citation on trust.

Everything here has been fetched and verified. Where a number appears in the design docs, it is quoted
from the paper below and the section it came from is named.

## Load-bearing

**Cemri et al., *Why Do Multi-Agent LLM Systems Fail?*** — arXiv preprint
[2503.13657](https://arxiv.org/abs/2503.13657)

The MAST taxonomy: 14 failure modes in 3 categories, developed from close analysis of 150 traces
(κ = 0.88 inter-annotator agreement) and applied to MAST-Data, 1,642 traces across 7 frameworks.
Supports:

- the failure-mode histogram `aoa diagnose` computes, which maps runs onto MAST directly;
- idempotency keys (ADR 010) — step repetition is **FM-1.3, 15.7%** of observed failures;
- the category split quoted in `design/architecture.md`: System Design 41.8%, Inter-Agent
  Misalignment 36.9%, Task Verification 21.3%;
- the failure-rate range quoted as motivation: **41% to 86.7% on 7 state-of-the-art open-source
  multi-agent systems**.

*Not a venue publication as of this writing — cite it as an arXiv preprint.*

**Vallecillos-Ruiz, Hort & Moonen, *Wisdom and Delusion of LLM Ensembles for Code Generation and
Repair*** — arXiv [2510.21513](https://arxiv.org/abs/2510.21513)

Supports the refusal of consensus voting (ADR 005). From the abstract: consensus-based strategies for
selecting solutions "fall into a 'popularity trap,' amplifying common but incorrect outputs", while a
diversity-based strategy "realizes up to 95% of this theoretical potential". A Gate is not a vote.

**Huang et al., *Large Language Models Cannot Self-Correct Reasoning Yet*** — ICLR 2024,
arXiv [2310.01798](https://arxiv.org/abs/2310.01798)

Supports keeping every LLM out of the verification path (ADR 002, ADR 011). From the abstract: LLMs
"struggle to self-correct their responses without external feedback, and at times, their performance
even degrades after self-correction." `aoa`'s Gate *is* the external feedback — a compiler and a test
suite, not a second opinion from the same model.

**Anthropic, *How we built our multi-agent research system*** —
[anthropic.com/engineering/multi-agent-research-system](https://www.anthropic.com/engineering/multi-agent-research-system)

Supports the flat orchestrator–worker topology (ADR 003) and the cost framing: a multi-agent system
costs substantially more tokens than a single agent, so fan-out has to earn its keep.

**Hua et al., *Shapley-Coop: Credit Assignment for Emergent Cooperation in Self-Interested LLM
Agents*** — arXiv [2506.07388](https://arxiv.org/abs/2506.07388)

Supports the refusal of market mechanisms (ADR 005) by delimiting where they apply: Shapley-style
pricing addresses credit assignment among *self-interested* agents. `aoa`'s workers are not
self-interested — they are instances of one model working toward one goal — so the problem the
mechanism solves does not arise here.

## Prior art for the merge queue

Not research, but the direct intellectual ancestry of the design:

- Graydon Hoare, [the not-rocket-science rule](https://graydon2.dreamwidth.org/1597.html) —
  *automatically maintain a repository of code that always passes all the tests.*
- [bors-ng](https://bors.tech/) — prevents "merge skew / semantic merge conflicts".
- [GitHub merge queue](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue)
  — keeps a branch "never broken by incompatible changes".
- [SWE-bench](https://arxiv.org/abs/2310.06770) and
  [SWE-bench-Live](https://swe-bench-live.github.io/) — the evaluation sets `aoa eval` targets.
  SWE-bench-Live exists because contamination inflates results on static sets; treat any headline
  resolve-rate accordingly.

## What is not here

The design was arrived at partly through a long exploratory phase — LLM research transcripts, an origin
prompt, a survey of swarm and market approaches (ant colony optimization, MAX-MIN Ant System, Contract
Net). None of that backs the current architecture: those approaches were **rejected**
([ADR 005](adr/005-no-markets-no-consensus.md), [ADR 011](adr/011-debate-markets-as-offline-tools.md)),
and transcripts are not evidence in any case.

Those files used to live in `docs/research/` and `docs/history/`. They were removed rather than kept
behind a disclaimer, because a reader should not have to work out which pages are load-bearing. They
remain in git history if anyone wants the road not taken:

```bash
git show 30fcc28 -- docs/research/    # the five LLM transcripts
git show cac011b -- docs/history/     # origin prompt + Gas Town snapshot
```
