# ADR 001: Event-Sourced Truth, Not a Mutable Store

## Status
Accepted

## Context
The system must survive agent crashes, support audit/replay, and let multiple workers progress without
a shared mutable database that they race to read and write. Prior `aoa` retained Dolt as a CQRS read
model; distributed-systems practice is that shared multi-agent memory must be treated as a
distributed-systems problem — giving agents full read/write to a monolithic store causes coherence decay
and "exponential coupling."

## Decision
An **append-only JSONL event log is the single source of truth**. All state — tickets, dependency
readiness, worker status, the merge queue — is a **pure fold** over the event stream. There is no
separate authoritative mutable store. Each event has an envelope `{seq, type, ts, actor, payload}`.

## Consequences
- Crash recovery and time-travel debugging come for free (replay to any `seq`).
- State is a deterministic function of events, so it is trivially testable.
- Coordination is "via shared state, not messaging" — the log *is* the blackboard.
- Tradeoff: the log grows append-only (acceptable for the single-node MVP; compaction/snapshots are a
  later optimization).

## Research basis
Event sourcing / log-as-truth (Kleppmann, *DDIA*); MAST Step-Repetition findings (Cemri et al.,
arXiv:2503.13657).
