# Research corpus

**Raw LLM research transcripts — kept for provenance, not as current documentation.**

These six files are unedited output from ChatGPT, Claude, Gemini, Grok and Perplexity, produced in June
2026 while the architecture was being argued out. They are cited by the [ADRs](../design/adr/README.md) and by
[`architecture.md`](../design/architecture.md) as the sources those decisions were weighed against.

**Do not read these as descriptions of how `aoa` works.** They are long, contradictory by design (that was
the point — several models arguing), sometimes wrong, and they predate essentially every line of the
current implementation. Some end mid-sentence or with the model offering to continue.

For what the system actually does:

- [`../getting-started.md`](../getting-started.md) — the tutorial
- [`../design/architecture.md`](../design/architecture.md) — the design
- [`../design/adr/`](../design/adr/README.md) — the decisions, with their reasoning

| File | Source | Note |
|---|---|---|
| [`chatgpt.md`](chatgpt.md) | ChatGPT | Opens mid-conversation; no framing. |
| [`claude-report.md`](claude-report.md) | Claude | Renamed from `claude.md`: on a case-insensitive filesystem that shadowed the repo's `CLAUDE.md`, so Claude Code silently loaded a 28 KB essay as project instructions. |
| [`gemini.md`](gemini.md) | Gemini | An independent critique; the least formatted of the six. |
| [`grok.md`](grok.md) | Grok | The longest; the shared-log/blackboard argument behind ADR 006 is here. |
| [`perplexity.md`](perplexity.md) | Perplexity | Mostly literature pointers. |
| [`links.md`](links.md) | — | The citation list. Renamed from `research_links.md`. |
