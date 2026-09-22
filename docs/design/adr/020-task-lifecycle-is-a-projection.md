# ADR 020: A Task's Lifecycle Is a Projection, and `aoa` Interoperates Rather Than Joins

## Status
Accepted. Extends [ADR 015](015-aoa-is-a-backend.md) (the backend contract) and
[ADR 012](012-observability-as-replay-projection.md) (what a replay projection is).
Bounds [ADR 019](019-reporting-back-belongs-to-the-front-door.md) by saying what a caller may
*read*, where that ADR said what `aoa` will not *send*. Names the reopen condition for
[#71](https://github.com/bharadwaj6/ageOfAgents/issues/71) that
[ADR 018](018-the-agent-is-not-confined.md) left general.

## Context
A front door cannot ask `aoa` "is this Goal done, and did it work?" and get an answer shaped like
anything else in the ecosystem. It polls `status --json`, reads `GoalView.Outcome`, and writes its own
predicate. The reference front door does exactly that: `examples/github-issues/aoa-github.sh` defines
`live` and `terminal` over `.outcome` in jq. Every front door re-derives them.

The outcome vocabulary is also lossy in one place that matters. `running` means both *a worker is
working* and *every task merged, the pull request just has not been opened yet* — the two branches at
`state.GoalOutcome`'s `inFlight` and `complete && g.Branch != ""`. Those are opposite things to tell a
requester, and nothing in the contract separates them.

Meanwhile the surrounding ecosystem has converged on a shape for this. Kubernetes-style **conditions** —
a type, a tri-state status, a reason, a message and the time it last changed — are how a long-running
system reports "what is true right now, and why" without inventing a state machine per consumer.

[google/ax](https://github.com/google/ax) (read at `d8ed0fe38bce`, 2026-09-20) is the case in point, and
it turns out to occupy the complementary half of this problem rather than the same half:

- **AX models the sandbox.** `Task`, `Workspace`, `Gateway`, `Model`; network fencing to an egress
  allowlist; CPU and memory limits; `ax suspend`/`ax resume` with a snapshotted volume. This is the gap
  ADR 018 admits `aoa` has and #71 asks for.
- **AX deliberately does not model the shape of the work.** Its `docs/concepts.md`: *"An agent is not
  one process that runs to completion; over its lifetime it plans, delegates, retries, and fans work
  out. AX does not try to model that shape."* There is no gate, no merge queue and no verification
  anywhere in its tree.
- **AX has no completion signal.** Its `docs/runner.md`: *"The control plane does not currently read
  the command's exit status back from the container."* Its conditions — `WorkspaceReady`,
  `GatewayReady`, `Ready` — describe whether the sandbox is up, not whether the work succeeded.
- **AX removed budget and approval policy.** `TaskSpec` field 9 is `reserved "policies"`, commented
  *"budget and approval config, removed for now"*. `aoa` has both shipped and recorded (ADR 017,
  ADR 008).

So the seam is clean in both directions: a runtime like AX can run an agent safely, and `aoa` can decide
whether what it produced is correct. What is missing is a vocabulary in which the second half can answer
the first.

Depending on AX to get that vocabulary would be the wrong trade. It requires a Kubernetes cluster,
Redis, `ko`, a container registry and a reachable Agent Substrate control API. It is `ax.io/v1alpha1`,
six months old, and its README warns: *"We are still actively refining our core concepts, protocols, and
specifications. We will likely to introduce major breaking changes prior to a stable release."* Golden
rule 6 — one static binary, one config file, git only — is not worth spending on a pre-stable API when
the thing we actually want from it is a JSON shape.

## Decision
**A Goal's lifecycle is published as conditions computed by replay. `aoa` speaks the shape other
runtimes already parse, and depends on none of them.**

1. **Lifecycle is a projection, not a new source of truth.** Conditions are a pure fold over the Event
   Log, like `metrics.Compute`, `diagnose.Classify` and `internal/otel`. No new event type, no side
   table, no field the Scheduler writes (ADR 001, ADR 012). `state.GoalOutcome` remains the single
   authority for the one-word phase; conditions add *why* and *when*, not a second opinion.

2. **The vocabulary is borrowed; the semantics are ours.** A condition carries `type`, `status`
   (`True`/`False`/`Unknown`), `reason`, `message` and `last_transition_time` — the fields of AX's
   `Condition` message and of the Kubernetes convention behind it. Four types are published:

   | Type | True when |
   |---|---|
   | `Accepted` | the Goal has at least one task; it is no longer merely queued |
   | `Verified` | every one of its tasks merged past the Gate |
   | `Delivered` | its branch was pushed and its pull request opened (`Unknown` outside PR mode) |
   | `Complete` | terminal: nothing moves without new input. **This is the one to wait on.** |

   `Verified` is the condition no general-purpose runtime has, and it is the whole point of `aoa`. A
   runtime can tell you a process exited; only the Gate can tell you the build and tests passed on the
   post-merge state.

3. **`Complete` settles what "done" means.** The codebase carries four near-synonyms — `state.Settled`,
   `StatusView.Settled`, `cmd/aoa.workSettled` and `state.GoalComplete` — which disagree at the edges
   about queued Goals, approval parking and success. `Complete` is defined as `StatusView.Settled`'s
   per-Goal rule, and it is the canonical answer to "is this Goal done". The other three keep their jobs
   and say in a doc comment what they are instead.

4. **Asking is supported; being told is not.** `aoa wait` blocks until `Complete` and exits with the
   outcome. This does **not** reverse ADR 019. That ADR refused *push*, because delivery to an addressee
   the log names needs per-destination credentials and a durable cursor with nowhere to live in a CLI. A
   read projection needs neither: it is addressee-free, cursorless, and computed on demand from the log.
   That is precisely the line ADR 019 drew, and a projection sits on the near side of it. ADR 019 is
   ratified as Accepted alongside this record.

5. **Deeper integration is captured, not built.** Two directions are written down with the evidence that
   would justify them, so the refusal stays falsifiable:

   | Deferred | Reopen when |
   |---|---|
   | Running each worker attempt as an AX `Task`, behind a new `agent.Backend` — confinement by delegation rather than by building microVM support (#71, ADR 018) | A real deployment takes goals whose text `aoa` does not control. ADR 018's answer — confinement is the operator's, and the sandbox is the machine you run on — would then be made first-class rather than replaced |
   | Speaking [A2A](https://a2a-protocol.org): `TaskState`, streaming updates and push notifications | The team transport ([#76](https://github.com/bharadwaj6/ageOfAgents/issues/76)) lands. This is already ADR 019's reopen condition; A2A only makes the reopened thing a standard instead of a bespoke webhook. A2A's `Task` maps onto a Goal, so it becomes a transport over this same fold |

## Consequences
- **The contract grows by addition.** `GoalView.Conditions` is a new field under the existing
  `ContractVersion = 1`, which ADR 015 §5 permits and `TestContractWireShape` pins. No consumer breaks;
  `Outcome` keeps its meaning and its seven values.
- **A front door stops writing predicates.** "Did it work" becomes `Complete=True` plus `Verified`,
  rather than a jq expression enumerating outcome strings that a later release might extend.
- **The overloaded `running` becomes readable** without changing it: all-merged-pending-delivery is
  `Verified=True, Delivered=False, reason=DeliveryPending`, and mid-flight is `Verified=False,
  reason=WorkInProgress`.
- **No dependency is added.** No Kubernetes, no gRPC client, no module in `go.mod`. Interoperating
  means emitting a shape, which costs a struct.
- **`aoa` stays a component.** This record does not make `aoa` a runtime, and does not make it
  something a runtime replaces. It makes the boundary legible in both directions, which is ADR 015's
  position applied to the return leg.
- **Tradeoff: four condition types is a guess.** They are derived from what `GoalOutcome` already
  distinguishes, not from a consumer's stated need, because there is one consumer today and it is ours.
  If a second integration needs a fifth, adding one is additive; the risk is publishing a vocabulary
  before it has been used in anger, and it is accepted deliberately in exchange for not making every
  caller invent its own.
- **Tradeoff: conditions duplicate information already derivable from the log.** That is what a
  projection is. The alternative — each consumer folding the log itself — is the situation this record
  ends.

## Research basis
Engineering judgement about where a boundary runs, resting on two primary sources read directly rather
than on a paper. The claims about AX are quoted from its own repository at `d8ed0fe38bce` — `docs/concepts.md`
(what it declines to model), `docs/runner.md` (no exit status read back) and `pkg/apis/v1alpha1/ax.proto`
(the `Condition` message, and `reserved "policies"` at `TaskSpec` field 9). The A2A task-state model is
from its published [specification](https://a2a-protocol.org/latest/specification/). The condition
convention itself predates both and comes from the Kubernetes API conventions, which ADR 003 already
cites as prior art for the reconcile loop.
