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
)

// modeOrder fixes the histogram's row order so output is deterministic.
var modeOrder = []Mode{
	StepRepetition,
	PrematureTermination,
	DeadDependencyStall,
	RetryChurn,
	WorkerStall,
	MissingVerification,
}

// modeDetail is the human-readable description shown beside each mode.
var modeDetail = map[Mode]string{
	StepRepetition:       "same logical ticket merged more than once (idempotency violation)",
	PrematureTermination: "ticket failed without delivering and without a dead dependency",
	DeadDependencyStall:  "ticket blocked or failed because a dependency can never complete",
	RetryChurn:           "proposals rejected by the Gate and re-attempted",
	WorkerStall:          "stall detector flagged a worker with no progress",
	MissingVerification:  "merge without a preceding VerificationPassed (invariant: must be 0)",
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
	)
	for _, e := range events {
		switch e.Type {
		case api.TicketCreated:
			var p api.TicketCreatedPayload
			if e.DecodePayload(&p) == nil && p.IdempotencyKey != "" {
				keyOf[p.TicketID] = p.IdempotencyKey
			}
		case api.VerificationPassed:
			var p api.VerificationPassedPayload
			if e.DecodePayload(&p) == nil {
				verified[p.TicketID] = true
			}
		case api.VerificationFailed:
			var p api.VerificationFailedPayload
			if e.DecodePayload(&p) == nil {
				verifFailed[p.TicketID]++
				retryEvents++
			}
		case api.Merged:
			var p api.MergedPayload
			if e.DecodePayload(&p) == nil {
				mergeByTicket[p.TicketID]++
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

	counts := map[Mode]int{
		StepRepetition:       len(stepRep),
		PrematureTermination: len(premature),
		DeadDependencyStall:  len(deadDep),
		RetryChurn:           retryEvents,
		WorkerStall:          len(stalled),
		MissingVerification:  len(missingVerif),
	}
	tickets := map[Mode][]string{
		StepRepetition:       slices.Collect(maps.Keys(stepRep)),
		PrematureTermination: premature,
		DeadDependencyStall:  deadDep,
		RetryChurn:           slices.Collect(maps.Keys(verifFailed)),
		WorkerStall:          slices.Collect(maps.Keys(stalled)),
		MissingVerification:  slices.Collect(maps.Keys(missingVerif)),
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
