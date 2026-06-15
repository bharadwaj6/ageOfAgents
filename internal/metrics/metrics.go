// Package metrics computes the success metrics from docs/design/metrics.md purely by
// replaying the Event Log — no bespoke instrumentation, in keeping with the
// design thesis that the log is the single source of truth. Every number here is
// a function of the event stream (plus, by construction, the fact that the
// Scheduler is deterministic Go: coordination uses zero LLM sessions).
package metrics

import (
	"sort"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Metrics is the replay-derived report for one run.
type Metrics struct {
	Goals                int            `json:"goals"`
	Tickets              int            `json:"tickets"`
	Merged               int            `json:"merged"`
	Failed               int            `json:"failed"`
	Decomposed           int            `json:"decomposed"`
	EmergentTickets      int            `json:"emergent_tickets"`       // created at runtime by a worker
	WorkerSessions       int            `json:"worker_sessions"`        // LLM work invocations (WorkStarted)
	CoordinationSessions int            `json:"coordination_sessions"`  // LLM calls for coordination — 0 by design
	TokensTotal          int            `json:"tokens_total"`           // LLM tokens across all work (0 for the mock backend)
	MergeCorrectness     float64        `json:"merge_correctness"`      // fraction of merges that passed the Gate first
	RejectedProposalRate float64        `json:"rejected_proposal_rate"` // rejected / (rejected + merged)
	StepRepetitions      int            `json:"step_repetitions"`       // merged tickets sharing an idempotency key
	MeanAttemptsToMerge  float64        `json:"mean_attempts_to_merge"`
	MaxConcurrentWorkers int            `json:"max_concurrent_workers"` // parallelism actually achieved
	CriticalPathDepth    int            `json:"critical_path_depth"`    // longest serial dependency chain
	DurationSeconds      float64        `json:"duration_seconds"`
	ThroughputPerMin     float64        `json:"throughput_per_min"`        // merged tickets per minute
	TokensByModel        map[string]int `json:"tokens_by_model,omitempty"` // tokens summed per model id (for per-model $ pricing)
	PerTicket            []TicketCost   `json:"per_ticket,omitempty"`      // per-ticket cost & latency breakdown
	PerGoal              []GoalCost     `json:"per_goal,omitempty"`        // per-goal cost & latency breakdown
}

// TicketCost is the per-ticket cost and latency breakdown, all derived by
// replaying the Event Log (where did the tokens and wall-clock go).
type TicketCost struct {
	TicketID        string  `json:"ticket_id"`
	GoalID          string  `json:"goal_id,omitempty"`
	Model           string  `json:"model,omitempty"`
	Tokens          int     `json:"tokens"`
	Attempts        int     `json:"attempts"`
	Status          string  `json:"status"`
	DurationSeconds float64 `json:"duration_seconds"` // first event mentioning the ticket → its terminal event
}

// GoalCost is the per-goal cost and latency breakdown.
type GoalCost struct {
	GoalID          string  `json:"goal_id"`
	Tokens          int     `json:"tokens"`
	Merged          int     `json:"merged"`
	Failed          int     `json:"failed"`
	DurationSeconds float64 `json:"duration_seconds"`
}

// USD converts a per-model token tally into a dollar cost using a price map of
// USD per *million* tokens. Models absent from the price map contribute 0, so an
// unpriced run reports $0 rather than a wrong number. Pure helper: pricing data
// stays in config, the arithmetic stays here.
func USD(tokensByModel map[string]int, pricePerMTok map[string]float64) float64 {
	var total float64
	for model, toks := range tokensByModel {
		total += float64(toks) / 1e6 * pricePerMTok[model]
	}
	return total
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
		// per-ticket / per-model breakdown (cost & latency)
		tokensByTicket  = map[string]int{}
		modelByTicket   = map[string]string{}
		tokensByModel   = map[string]int{}
		firstTSByTicket = map[string]time.Time{}
		lastTSByTicket  = map[string]time.Time{}
	)
	for i, e := range events {
		if i == 0 {
			firstTS = e.Timestamp
		}
		lastTS = e.Timestamp
		id := e.TicketID()
		if id != "" {
			if _, seen := firstTSByTicket[id]; !seen {
				firstTSByTicket[id] = e.Timestamp
			}
			lastTSByTicket[id] = e.Timestamp
		}

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
				var p api.ProposalSubmittedPayload
				if e.DecodePayload(&p) == nil {
					m.TokensTotal += p.Tokens
					tokensByTicket[id] += p.Tokens
					if p.Model != "" {
						modelByTicket[id] = p.Model
						tokensByModel[p.Model] += p.Tokens
					}
				}
			}
			if e.Type == api.TicketDecomposed {
				var p api.TicketDecomposedPayload
				if e.DecodePayload(&p) == nil {
					m.TokensTotal += p.Tokens
					tokensByTicket[id] += p.Tokens
					if p.Model != "" {
						modelByTicket[id] = p.Model
						tokensByModel[p.Model] += p.Tokens
					}
				}
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

	if len(tokensByModel) > 0 {
		m.TokensByModel = tokensByModel
	}
	m.PerTicket, m.PerGoal = breakdown(s, tokensByTicket, modelByTicket, firstTSByTicket, lastTSByTicket)
	return m
}

// breakdown builds the per-ticket and per-goal cost & latency views from the
// folded state plus the per-ticket token/timestamp tallies gathered in Compute.
// Sorted by ID for deterministic output.
func breakdown(s *state.State, tokensByTicket map[string]int, modelByTicket map[string]string, firstTS, lastTS map[string]time.Time) ([]TicketCost, []GoalCost) {
	ids := make([]string, 0, len(s.Tickets))
	for id := range s.Tickets {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	type gacc struct {
		tokens, merged, failed int
		first, last            time.Time
	}
	goals := map[string]*gacc{}

	perTicket := make([]TicketCost, 0, len(ids))
	for _, id := range ids {
		t := s.Tickets[id]
		tc := TicketCost{
			TicketID:        id,
			GoalID:          t.GoalID,
			Model:           modelByTicket[id],
			Tokens:          tokensByTicket[id],
			Attempts:        t.Attempts,
			Status:          string(t.Status),
			DurationSeconds: span(firstTS[id], lastTS[id]),
		}
		perTicket = append(perTicket, tc)

		g := goals[t.GoalID]
		if g == nil {
			g = &gacc{}
			goals[t.GoalID] = g
		}
		g.tokens += tc.Tokens
		switch t.Status {
		case state.StatusMerged:
			g.merged++
		case state.StatusFailed:
			g.failed++
		}
		if f := firstTS[id]; !f.IsZero() && (g.first.IsZero() || f.Before(g.first)) {
			g.first = f
		}
		if l := lastTS[id]; l.After(g.last) {
			g.last = l
		}
	}

	gids := make([]string, 0, len(goals))
	for gid := range goals {
		gids = append(gids, gid)
	}
	sort.Strings(gids)
	perGoal := make([]GoalCost, 0, len(gids))
	for _, gid := range gids {
		g := goals[gid]
		perGoal = append(perGoal, GoalCost{
			GoalID:          gid,
			Tokens:          g.tokens,
			Merged:          g.merged,
			Failed:          g.failed,
			DurationSeconds: span(g.first, g.last),
		})
	}
	return perTicket, perGoal
}

// span returns the seconds between two event timestamps, or 0 when they are
// absent or not strictly ordered (e.g. a single-event ticket).
func span(first, last time.Time) float64 {
	if first.IsZero() || !last.After(first) {
		return 0
	}
	return last.Sub(first).Seconds()
}

// GraphShape summarizes the emergent task-graph shape for one Goal, derived
// purely from replayed state. It makes runaway/wide decompositions visible —
// MaxFanOut here is exactly the quantity the orchestrator's MaxFanOut governor
// bounds, so the cap's effect can be read straight off `aoa status`.
type GraphShape struct {
	GoalID    string `json:"goal_id"`
	Tickets   int    `json:"tickets"`     // tickets belonging to this Goal
	MaxDepth  int    `json:"max_depth"`   // deepest decomposition depth reached (max Ticket.Depth)
	MaxFanOut int    `json:"max_fan_out"` // widest single decomposition (max len(Ticket.Children))
}

// GraphShapes returns the per-Goal task-graph shape, sorted by GoalID for
// deterministic output. It is a pure function of the derived state.
func GraphShapes(s *state.State) []GraphShape {
	byGoal := map[string]*GraphShape{}
	for _, t := range s.Tickets {
		gs := byGoal[t.GoalID]
		if gs == nil {
			gs = &GraphShape{GoalID: t.GoalID}
			byGoal[t.GoalID] = gs
		}
		gs.Tickets++
		if t.Depth > gs.MaxDepth {
			gs.MaxDepth = t.Depth
		}
		if n := len(t.Children); n > gs.MaxFanOut {
			gs.MaxFanOut = n
		}
	}
	out := make([]GraphShape, 0, len(byGoal))
	for _, gs := range byGoal {
		out = append(out, *gs)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GoalID < out[j].GoalID })
	return out
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
