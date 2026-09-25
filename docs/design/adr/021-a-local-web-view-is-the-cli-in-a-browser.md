# ADR 021: A Local Web View Is the CLI in a Browser

## Status
Accepted. Amends one consequence of [ADR 015](015-aoa-is-a-backend.md) — "`aoa` does not grow chat
integrations, a board UI or a triage model" — for a view bounded as below; the rest of ADR 015 stands.
Leaves [#76](https://github.com/bharadwaj6/ageOfAgents/issues/76), the team transport, deferred, and so
leaves [ADR 019](019-reporting-back-belongs-to-the-front-door.md)'s reopen condition unfired.

## Context
Watching one Goal execute is harder than it should be. `aoa status --watch` redraws a terminal summary
and `aoa events --follow` prints the log line by line. Neither puts one Goal's whole story in one
place: its conditions, its task graph, each attempt, the Gate's output when a proposal was rejected, what
it cost, and the parked proposal waiting for a decision. Anyone who wants that today folds
`status --json` and `events --json` themselves.

ADR 015 refused a board UI so that `aoa` would not compete with front doors on intake, triage and
prioritisation, and would not grow a second product surface. That refusal was about **deciding what gets
worked on**. A view of work already submitted decides nothing. It is `aoa status` in HTML, and it uses
the same verbs a person at the terminal already has.

The roadmap puts "a dashboard over the Event Log" under #76, the team transport, together with auth,
multiple users and durable server state. A local, single-user view needs none of those.

## Decision
**`aoa ui` serves a web view of one workspace to the person on this machine. It is the CLI's read and
write verbs over HTTP, and nothing more.**

1. **Reads are existing projections.** `/api/status` returns `aoa status --json` byte for byte (the
   same `statusView`). The live stream is `aoa events --json --since N --follow` framed as Server-Sent
   Events, with the same resume rule behind it (one shared `tailLog`). A Goal's timeline is its events
   filtered by `goal_id` and by its tasks' `ticket_id`. There is no new event type, no cache and no side
   table ([ADR 001](001-event-sourced-truth.md), [ADR 012](012-observability-as-replay-projection.md)).
   The browser never folds the log. The server's projection is the one authority.
2. **Writes are the CLI verbs, and nothing else.** Submit, amend, cancel, approve and reject call the
   same functions as `aoa goal`, `amend`, `cancel`, `approve` and `reject`, with the same checks and the
   same `pkg/api` result types. The page therefore has exactly a CLI user's authority (ADR 015 §3). It
   records its goals with source `ui` and `--by` as given, and it never guesses an identity.
3. **It never runs the Scheduler.** There is no Run button and no scheduler control. It does not probe
   the scheduler lock, because taking that lock even briefly could make a concurrent `aoa run` exit 75.
   Execution stays with `aoa run`, so there is still exactly one control loop
   ([ADR 003](003-flat-orchestrator-worker.md)).
4. **It is local by construction.** It listens on loopback by default. On loopback it answers only
   requests addressed to a loopback hostname, which defeats DNS rebinding. Writes need a JSON body and a
   same-origin request, so a cross-site form or fetch cannot drive them, and every response forbids
   framing. A non-loopback address is refused unless `--read-only` is set; remote use goes over an SSH
   tunnel. There is no auth and no user model, because there is one user: whoever can already run `aoa`
   here.
5. **It ships inside the binary.** It uses the standard library's `net/http` and `embed`, plus
   hand-written HTML, CSS and JavaScript with no build step, no npm and nothing fetched from a CDN. The
   binary still works offline, and there is no new module in `go.mod` (golden rule 6).
6. **Still refused:** intake from other systems, triage, prioritisation, board sync, notifications,
   multiple users and a durable server. Those are a front door's job (ADR 015) or #76's, and this record
   reopens none of them.

## Consequences
- **One Goal's execution is readable in one place, live.** That covers its conditions with their
  reasons and transition times, tasks indented by decomposition depth, the Gate output behind each
  failure, a per-Goal event timeline, and the approve and reject buttons where a proposal is parked.
- **The contract is unchanged.** The HTTP responses are the existing `pkg/api` types. The only new
  shape is `/api/info`, which is page plumbing (the workspace, whether it is read-only, and `--by`).
  It is not contract, and front doors should keep using the CLI.
- **ADR 015's consequence is narrowed, not dropped.** "No board UI" now reads "no UI that decides what
  gets worked on". A view that adds authority, state or a control loop would still need its own record.
- **Goal text and Gate output are untrusted** (ADR 015 §8), so the page renders all log text as text and
  never as markup. A test enforces this.
- **Tradeoff: every refresh re-folds the log.** `/api/status` replays the whole Event Log per request,
  as `aoa wait` does per poll. The page debounces refreshes on the event stream, so a busy run causes a
  few folds a second, not one per event. An incremental fold is the fix if a large log makes this
  noticeable.
- **Tradeoff: a second surface to keep in step.** A new field in `StatusView` shows up in the JSON at
  once, but the page only displays it once someone renders it. The page reads the contract and does not
  extend it, so it can lag behind the contract but never contradict it.

## Research basis
Engineering judgement about where ADR 015's line actually runs, not evidence from a paper. The security
posture follows standard guidance for local HTTP servers: bind to loopback, check the Host header
against DNS rebinding, require a non-simple content type so cross-origin writes need a preflight that
is never granted, and set a restrictive Content-Security-Policy.
