// Package metrics computes the success metrics from docs/v2/metrics.md purely by
// replaying the Event Log — no bespoke instrumentation, in keeping with the
// design thesis that the log is the single source of truth. Every number here is
// a function of the event stream (plus, by construction, the fact that the
// Scheduler is deterministic Go: coordination uses zero LLM sessions).
package metrics

import (
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Metrics is the replay-derived report for one run.
type Metrics struct {
	Goals                int     `json:"goals"`
	Tickets              int     `json:"tickets"`
	Merged               int     `json:"merged"`
	Failed               int     `json:"failed"`
	Decomposed           int     `json:"decomposed"`
	EmergentTickets      int     `json:"emergent_tickets"`       // created at runtime by a worker
	WorkerSessions       int     `json:"worker_sessions"`        // LLM work invocations (WorkStarted)
	CoordinationSessions int     `json:"coordination_sessions"`  // LLM calls for coordination — 0 by design
	MergeCorrectness     float64 `json:"merge_correctness"`      // fraction of merges that passed the Gate first
	RejectedProposalRate float64 `json:"rejected_proposal_rate"` // rejected / (rejected + merged)
	StepRepetitions      int     `json:"step_repetitions"`       // merged tickets sharing an idempotency key
	MeanAttemptsToMerge  float64 `json:"mean_attempts_to_merge"`
	MaxConcurrentWorkers int     `json:"max_concurrent_workers"` // parallelism actually achieved
	CriticalPathDepth    int     `json:"critical_path_depth"`    // longest serial dependency chain
	DurationSeconds      float64 `json:"duration_seconds"`
	ThroughputPerMin     float64 `json:"throughput_per_min"` // merged tickets per minute
}

// Compute derives the metrics for an event history. It is a pure function.
func Compute(events []api.Event) Metrics {
	var m Metrics
	s, err := state.Fold(events)
	if err != nil {
		return m // a non-replayable log yields the zero report; invariants catch this
	}

	m.Goals = len(s.Goals)
	m.Tickets = len(s.Tickets)
	for _, t := range s.Tickets {
		switch t.Status {
		case state.StatusMerged:
			m.Merged++
		case state.StatusFailed:
			m.Failed++
		case state.StatusDecomposed:
			m.Decomposed++
		}
	}

	// Walk the stream once for the event-shaped metrics.
	var (
		merges, mergesVerified, rejected int
		propSeq                          = map[string]int{}
		passSeq                          = map[string]int{}
		active                           = map[string]bool{}
		keyOfTicket                      = map[string]string{}
		mergedKeys                       = map[string]int{}
		attemptsSum, attemptsCount       int
		firstTS, lastTS                  time.Time
	)
	for i, e := range events {
		if i == 0 {
			firstTS = e.Timestamp
		}
		lastTS = e.Timestamp
		id := payloadTicketID(e)

		switch e.Type {
		case api.TicketCreated:
			var p api.TicketCreatedPayload
			if e.DecodePayload(&p) == nil {
				if p.CreatedBy != "" {
					m.EmergentTickets++
				}
				if p.IdempotencyKey != "" {
					keyOfTicket[p.TicketID] = p.IdempotencyKey
				}
			}
		case api.WorkStarted:
			m.WorkerSessions++
			active[id] = true
			if len(active) > m.MaxConcurrentWorkers {
				m.MaxConcurrentWorkers = len(active)
			}
		case api.ProposalSubmitted, api.TicketDecomposed, api.TicketFailed, api.WorkerRestarted, api.WorkerStalled:
			delete(active, id)
			if e.Type == api.ProposalSubmitted {
				propSeq[id] = e.Seq
			}
		case api.VerificationPassed:
			passSeq[id] = e.Seq
		case api.VerificationFailed:
			rejected++
		case api.Merged:
			merges++
			if passSeq[id] != 0 && passSeq[id] >= propSeq[id] {
				mergesVerified++
			}
			if key := keyOfTicket[id]; key != "" {
				mergedKeys[key]++
				if mergedKeys[key] > 1 {
					m.StepRepetitions++
				}
			}
		}
	}

	if merges > 0 {
		m.MergeCorrectness = float64(mergesVerified) / float64(merges)
	} else {
		m.MergeCorrectness = 1 // nothing merged, nothing to violate
	}
	if denom := rejected + merges; denom > 0 {
		m.RejectedProposalRate = float64(rejected) / float64(denom)
	}

	for _, t := range s.Tickets {
		if t.Status == state.StatusMerged {
			attemptsSum += t.Attempts
			attemptsCount++
		}
	}
	if attemptsCount > 0 {
		m.MeanAttemptsToMerge = float64(attemptsSum) / float64(attemptsCount)
	}

	m.CriticalPathDepth = criticalPathDepth(s)

	if !lastTS.IsZero() && lastTS.After(firstTS) {
		m.DurationSeconds = lastTS.Sub(firstTS).Seconds()
		if mins := m.DurationSeconds / 60; mins > 0 {
			m.ThroughputPerMin = float64(m.Merged) / mins
		}
	}
	return m
}

// criticalPathDepth returns the length of the longest dependency chain in the
// final graph (the minimum serial depth the work requires). Memoized DFS over a
// DAG (acyclicity is enforced elsewhere).
func criticalPathDepth(s *state.State) int {
	depth := map[string]int{}
	var d func(id string) int
	d = func(id string) int {
		if v, ok := depth[id]; ok {
			return v
		}
		t := s.Tickets[id]
		if t == nil {
			return 0
		}
		best := 0
		for _, dep := range t.DependsOn {
			if c := d(dep); c > best {
				best = c
			}
		}
		depth[id] = best + 1
		return depth[id]
	}
	max := 0
	for id := range s.Tickets {
		if c := d(id); c > max {
			max = c
		}
	}
	return max
}

// payloadTicketID extracts ticket_id from any payload carrying one.
func payloadTicketID(e api.Event) string {
	if len(e.Payload) == 0 {
		return ""
	}
	var p struct {
		TicketID string `json:"ticket_id"`
	}
	if e.DecodePayload(&p) != nil {
		return ""
	}
	return p.TicketID
}
