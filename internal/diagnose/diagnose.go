// Package diagnose classifies a run's MAST-style failure modes purely by
// replaying the Event Log. Where docs/design/architecture.md argues aoa is
// *aligned* with the MAST taxonomy (Cemri et al., arXiv:2503.13657), this package
// lets a run be *measured* against it: it turns the event stream into a
// failure-mode histogram, so "we designed around MAST" becomes a checkable
// property of every run rather than a claim in prose.
//
// Like internal/metrics and internal/invariant, every number here is a pure
// function of the events — there is no bespoke instrumentation, in keeping with
// the design thesis that the log is the single source of truth (ADR 001).
package diagnose

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Mode is a failure mode drawn from the MAST taxonomy, specialized to the
// signals aoa's Event Log makes observable.
type Mode string

const (
	// StepRepetition (MAST FM-1.3): the same logical work was merged more than
	// once — the single largest MAST failure mode, an idempotency violation.
	StepRepetition Mode = "step_repetition"
	// PrematureTermination: a ticket gave up (failed on its own attempts) without
	// delivering, even though nothing structurally prevented completion.
	PrematureTermination Mode = "premature_termination"
	// DeadDependencyStall: a ticket can never complete because a dependency
	// terminally failed (a coordination/inter-agent-misalignment symptom).
	DeadDependencyStall Mode = "dead_dependency_stall"
	// RetryChurn: proposals were rejected by the Gate and re-attempted — wasted
	// work that, unbounded, becomes step repetition.
	RetryChurn Mode = "retry_churn"
	// WorkerStall: the stall detector flagged a worker making no progress.
	WorkerStall Mode = "worker_stall"
	// MissingVerification: a merge occurred without a prior VerificationPassed.
	// Should always be 0 — the MergeImpliesVerified invariant forbids it by
	// construction; we still measure it as a defence-in-depth signal.
	MissingVerification Mode = "missing_verification"
	// ReplayError: the log could not be folded into state. Surfaced as its own
	// finding so a corrupt history is never silently scored as healthy.
	ReplayError Mode = "replay_error"

	// --- Deterministic-orchestration taxonomy ---------------------------------
	// Mature systems stop failing for their original reasons and start failing
	// for new ones. These are the failure modes a *deterministic* control plane
	// creates — distinct from the MAST modes above, but measured the same way
	// (pure functions of the log). All are 0 on a healthy, settled run.

	// QueueStarvation: a ticket became dispatchable (Ready) but the run ended
	// with it still unclaimed — work that never got a worker slot.
	QueueStarvation Mode = "queue_starvation"
	// SchedulerDeadlock: non-terminal work is stuck mid-pipeline at end of run
	// (pending/claimed/running/proposed) with no dead dependency to explain it —
	// the orchestrator's "made no progress but work is unsettled" condition.
	SchedulerDeadlock Mode = "scheduler_deadlock"
	// RetryLivelock: a ticket hit the crash-loop ceiling — the same failure
	// repeated until the governor gave up (see retry backoff / crash-loop, #6).
	RetryLivelock Mode = "retry_livelock"
	// VerificationBlindSpot: a merge passed the Gate but a broader shadow test
	// set rejected it — the regression-escape signal (#8). The dangerous one: the
	// Gate was green but insufficient.
	VerificationBlindSpot Mode = "verification_blind_spot"
	// StaleSpecDrift: a worker was in-flight (running) when its Goal was amended,
	// so it proceeded against a now-superseded spec (mid-run goal amendment, #11).
	StaleSpecDrift Mode = "stale_spec_drift"
	// FlakyGate: the Gate returned both a pass and a failure for the *same*
	// verified content (identical tree hash) — so it is not a function of the
	// code alone, and a merge it allowed may rest on a verdict it had already
	// contradicted. The crash-loop governor keys on repeated *identical*
	// failures, which is the opposite signal: a verdict that flips never trips
	// it. See flakyGate for what this can and cannot prove.
	FlakyGate Mode = "flaky_gate"
)

// modeOrder fixes the histogram's row order so output is deterministic.
var modeOrder = []Mode{
	StepRepetition,
	PrematureTermination,
	DeadDependencyStall,
	RetryChurn,
	WorkerStall,
	MissingVerification,
	QueueStarvation,
	SchedulerDeadlock,
	RetryLivelock,
	VerificationBlindSpot,
	StaleSpecDrift,
	FlakyGate,
}

// modeDetail is the human-readable description shown beside each mode.
var modeDetail = map[Mode]string{
	StepRepetition:        "same logical ticket merged more than once (idempotency violation)",
	PrematureTermination:  "ticket failed without delivering and without a dead dependency",
	DeadDependencyStall:   "ticket blocked or failed because a dependency can never complete",
	RetryChurn:            "proposals rejected by the Gate and re-attempted",
	WorkerStall:           "stall detector flagged a worker with no progress",
	MissingVerification:   "merge without a preceding VerificationPassed (invariant: must be 0)",
	QueueStarvation:       "ticket left Ready (dispatchable) but never claimed by a worker",
	SchedulerDeadlock:     "non-terminal work stuck mid-pipeline at end of run, no dead dependency",
	RetryLivelock:         "ticket hit the crash-loop ceiling (same failure repeated until giving up)",
	VerificationBlindSpot: "merge passed the Gate but a broader shadow test set rejected it",
	StaleSpecDrift:        "worker was running when its Goal was amended (proceeding against a stale spec)",
	FlakyGate:             "the Gate both passed and failed the same verified content (nondeterministic Gate)",
}

// crashLoopPrefix marks a TicketFailed reason the crash-loop governor emitted.
const crashLoopPrefix = "crash loop:"

// gateRejectionPrefix is how mergequeue.verifyFailureReason words a real Gate
// verdict on the code. Every other rejection reason — "gate could not run
// (sandbox failure): …", a merge conflict, a cancelled Goal — says nothing about
// the proposal, and counting those as verdicts would rebuild exactly the false
// positives the first Gate-precision sweep tripped over (issue #103, where a
// stopped Docker daemon read as a rejection).
const gateRejectionPrefix = "verification failed: "

// isGateRejection reports whether a failure reason is the Gate's own verdict on
// the code, seeing through the crash-loop governor's wrapper.
func isGateRejection(reason string) bool {
	return strings.HasPrefix(strings.TrimPrefix(reason, crashLoopPrefix+" "), gateRejectionPrefix)
}

// gateVerdicts records, per verified tree, which verdicts the Gate returned for
// it and which tickets were involved.
type gateVerdicts struct {
	passed, failed bool
	tickets        map[string]bool
}

// flakyGate returns the tickets whose Gate verdicts contradicted themselves: the
// same tree hash — the same content, byte for byte — both passed and failed.
//
// What that proves: the Gate is not a function of the content it runs on. Within
// one run that is a nondeterministic Gate, which is the mechanism issue #104
// describes — a flaky test lets a lucky retry merge a patch an earlier run
// rejected, so "nothing merges that fails the Gate" (ADR 002) quietly stops
// meaning what it says.
//
// What it does not prove, and must not be read as:
//
//   - It cannot say which verdict was right, or that the merged code is broken.
//     A flagged ticket is a candidate for a human to look at, not a defect.
//   - A count of 0 is *not* evidence of a deterministic Gate. Only the provable
//     subset is visible: a retry runs the agent again from scratch, and any
//     change at all to the patch gives a different tree, which this cannot
//     compare. That case is common, and invisible here. Measuring the *rate* of
//     flakiness needs the Gate re-run deliberately on identical content (the
//     `confirm_runs` half of #104), which is a behaviour change, not a
//     projection.
//   - Two runs of the same content can legitimately disagree when something
//     outside the content changed between them: the gate commands were edited in
//     aoa.toml, the toolchain or sandbox image moved, a test timed out under
//     load, or a test reached the network. Infrastructure failures the Gate
//     itself recognised are already excluded (isGateRejection); the rest are
//     real false positives and are why this reports a finding, not a violation.
//   - A batch Gate run (mergequeue.ProcessBatch) that fails is re-isolated
//     per proposal and records no verdict of its own, so a contradiction between
//     a batch run and an isolated one is not visible.
func flakyGate(verdicts map[string]*gateVerdicts) []string {
	flagged := map[string]bool{}
	for _, v := range verdicts {
		if !v.passed || !v.failed {
			continue
		}
		for id := range v.tickets {
			flagged[id] = true
		}
	}
	return slices.Collect(maps.Keys(flagged))
}

// Finding is one row of the failure-mode histogram.
type Finding struct {
	Mode    Mode     `json:"mode"`
	Count   int      `json:"count"`
	Tickets []string `json:"tickets,omitempty"`
	Detail  string   `json:"detail"`
}

// Report is the full MAST-mode histogram for a run.
type Report struct {
	Findings []Finding `json:"findings"`
}

// Total returns the summed count across all findings — 0 means a clean run.
func (r Report) Total() int {
	n := 0
	for _, f := range r.Findings {
		n += f.Count
	}
	return n
}

// Classify replays the events and returns the MAST-mode histogram. It is a pure
// function: the same log always yields the same report.
func Classify(events []api.Event) Report {
	s, err := state.Fold(events)
	if err != nil {
		return Report{Findings: []Finding{{
			Mode:   ReplayError,
			Count:  1,
			Detail: fmt.Sprintf("event log is not replayable: %v", err),
		}}}
	}

	// Stream-derived signals (single pass, in seq order).
	var (
		keyOf          = map[string]string{} // ticket id -> idempotency key
		verified       = map[string]bool{}   // saw VerificationPassed for ticket
		mergedKeyCount = map[string]int{}    // idempotency key -> times merged
		mergeByTicket  = map[string]int{}    // ticket id -> times merged
		verifFailed    = map[string]int{}    // ticket id -> rejections
		stalled        = map[string]bool{}   // tickets the stall detector flagged
		missingVerif   = map[string]bool{}   // merges with no prior verification
		retryEvents    int
		blindSpot      []string                     // tickets that escaped a broader verifier (#8)
		failReason     = map[string]string{}        // ticket id -> latest TicketFailed reason
		goalOf         = map[string]string{}        // ticket id -> goal id
		running        = map[string]bool{}          // tickets currently in-flight (WorkStarted, not yet terminal)
		driftSet       = map[string]bool{}          // tickets running when their Goal was amended (#11)
		verdicts       = map[string]*gateVerdicts{} // verified tree -> the Gate's verdicts on it
	)
	// record files one Gate verdict under the tree it ran against. An empty tree
	// (a log written before the field existed, or a rejection with no Gate run)
	// is not comparable with anything and is dropped.
	record := func(tree, ticket string, passed bool) {
		if tree == "" {
			return
		}
		v := verdicts[tree]
		if v == nil {
			v = &gateVerdicts{tickets: map[string]bool{}}
			verdicts[tree] = v
		}
		if passed {
			v.passed = true
		} else {
			v.failed = true
		}
		v.tickets[ticket] = true
	}
	for _, e := range events {
		switch e.Type {
		case api.TicketCreated:
			var p api.TicketCreatedPayload
			if e.DecodePayload(&p) == nil {
				if p.IdempotencyKey != "" {
					keyOf[p.TicketID] = p.IdempotencyKey
				}
				goalOf[p.TicketID] = p.GoalID
			}
		case api.WorkStarted:
			running[e.TicketID()] = true
		case api.GoalAmended:
			// Any worker in-flight for this goal is now building against a stale
			// spec — the amendment supersedes what it was told.
			var p api.GoalAmendedPayload
			if e.DecodePayload(&p) == nil {
				for tid := range running {
					if goalOf[tid] == p.GoalID {
						driftSet[tid] = true
					}
				}
			}
		case api.VerificationPassed:
			var p api.VerificationPassedPayload
			if e.DecodePayload(&p) == nil {
				verified[p.TicketID] = true
				record(p.Tree, p.TicketID, true)
			}
		case api.VerificationFailed:
			var p api.VerificationFailedPayload
			if e.DecodePayload(&p) == nil {
				verifFailed[p.TicketID]++
				retryEvents++
				delete(running, p.TicketID)
				if isGateRejection(p.Reason) {
					record(p.Tree, p.TicketID, false)
				}
			}
		case api.ProposalSubmitted:
			delete(running, e.TicketID()) // work submitted; no longer in-flight
		case api.Merged:
			var p api.MergedPayload
			if e.DecodePayload(&p) == nil {
				mergeByTicket[p.TicketID]++
				delete(running, p.TicketID)
				if !verified[p.TicketID] {
					missingVerif[p.TicketID] = true
				}
				if k := keyOf[p.TicketID]; k != "" {
					mergedKeyCount[k]++
				}
			}
		case api.WorkerStalled:
			var p api.WorkerStalledPayload
			if e.DecodePayload(&p) == nil {
				stalled[p.TicketID] = true
			}
		case api.TicketFailed:
			var p api.TicketFailedPayload
			if e.DecodePayload(&p) == nil {
				failReason[p.TicketID] = p.Reason
				delete(running, p.TicketID)
				if isGateRejection(p.Reason) {
					record(p.Tree, p.TicketID, false)
				}
			}
		case api.WorkerRestarted, api.TicketDecomposed:
			delete(running, e.TicketID()) // attempt ended; no longer in-flight
		case api.RegressionEscaped:
			var p api.RegressionEscapedPayload
			if e.DecodePayload(&p) == nil {
				blindSpot = append(blindSpot, p.TicketID)
			}
		}
	}

	// Step repetition: a ticket merged twice, or two tickets sharing a key both
	// merged. Collect the affected ticket ids.
	stepRep := map[string]bool{}
	for id, n := range mergeByTicket {
		if n > 1 {
			stepRep[id] = true
		}
	}
	for id, key := range keyOf {
		if mergedKeyCount[key] > 1 && mergeByTicket[id] > 0 {
			stepRep[id] = true
		}
	}

	// State-derived signals: classify every failed ticket as either caused by a
	// dead dependency or a premature give-up, and add any still-blocked tickets
	// (an incomplete run) to the dead-dependency bucket.
	var premature, deadDep []string
	deadSeen := map[string]bool{}
	for _, t := range s.Tickets {
		if t.Status == state.StatusFailed {
			if s.DeadDependency(t) != "" {
				if !deadSeen[t.ID] {
					deadDep = append(deadDep, t.ID)
					deadSeen[t.ID] = true
				}
			} else {
				premature = append(premature, t.ID)
			}
		}
	}
	for _, t := range s.Blocked() {
		if !deadSeen[t.ID] {
			deadDep = append(deadDep, t.ID)
			deadSeen[t.ID] = true
		}
	}

	// Deterministic-orchestration taxonomy: leftover non-terminal tickets at end
	// of run are a stuck-scheduler symptom — Ready ones starved for a slot, the
	// rest deadlocked mid-pipeline (excluding dead-dep stalls already counted and
	// Awaiting tickets parked for approval, which is intentional, not a failure).
	var starved, deadlocked []string
	for _, t := range s.Tickets {
		switch t.Status {
		case state.StatusReady:
			starved = append(starved, t.ID)
		case state.StatusPending, state.StatusClaimed, state.StatusRunning, state.StatusProposed:
			if !deadSeen[t.ID] {
				deadlocked = append(deadlocked, t.ID)
			}
		}
	}
	// Retry livelock: tickets the crash-loop governor terminated (#6).
	var livelock []string
	for id, reason := range failReason {
		if strings.HasPrefix(reason, crashLoopPrefix) {
			livelock = append(livelock, id)
		}
	}

	// Flaky Gate: the same content drew both verdicts (see flakyGate).
	flaky := flakyGate(verdicts)

	counts := map[Mode]int{
		StepRepetition:        len(stepRep),
		PrematureTermination:  len(premature),
		DeadDependencyStall:   len(deadDep),
		RetryChurn:            retryEvents,
		WorkerStall:           len(stalled),
		MissingVerification:   len(missingVerif),
		QueueStarvation:       len(starved),
		SchedulerDeadlock:     len(deadlocked),
		RetryLivelock:         len(livelock),
		VerificationBlindSpot: len(blindSpot),
		StaleSpecDrift:        len(driftSet),
		FlakyGate:             len(flaky),
	}
	tickets := map[Mode][]string{
		StepRepetition:        slices.Collect(maps.Keys(stepRep)),
		PrematureTermination:  premature,
		DeadDependencyStall:   deadDep,
		RetryChurn:            slices.Collect(maps.Keys(verifFailed)),
		WorkerStall:           slices.Collect(maps.Keys(stalled)),
		MissingVerification:   slices.Collect(maps.Keys(missingVerif)),
		QueueStarvation:       starved,
		SchedulerDeadlock:     deadlocked,
		RetryLivelock:         livelock,
		VerificationBlindSpot: blindSpot,
		StaleSpecDrift:        slices.Collect(maps.Keys(driftSet)),
		FlakyGate:             flaky,
	}

	out := Report{Findings: make([]Finding, 0, len(modeOrder))}
	for _, m := range modeOrder {
		ts := tickets[m]
		sort.Strings(ts)
		out.Findings = append(out.Findings, Finding{
			Mode:    m,
			Count:   counts[m],
			Tickets: ts,
			Detail:  modeDetail[m],
		})
	}
	return out
}
