// Package state derives the Scheduler's state by replaying the Event Log. The
// replay is a pure function of the event stream (docs/design/adr/001-event-sourced-truth.md):
// there is no separate authoritative store to keep consistent.
package state

import (
	"fmt"
	"strings"
	"time"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// TicketStatus is the lifecycle stage of a ticket.
type TicketStatus string

const (
	StatusPending    TicketStatus = "pending"    // created; dependencies not yet satisfied
	StatusReady      TicketStatus = "ready"      // dependencies satisfied; dispatchable
	StatusClaimed    TicketStatus = "claimed"    // a worker took ownership
	StatusRunning    TicketStatus = "running"    // worker is executing
	StatusProposed   TicketStatus = "proposed"   // proposal submitted; awaiting merge queue
	StatusAwaiting   TicketStatus = "awaiting"   // verified; parked for human approval (ADR 008)
	StatusMerged     TicketStatus = "merged"     // verified and merged (terminal, success)
	StatusFailed     TicketStatus = "failed"     // gave up (terminal, failure)
	StatusDecomposed TicketStatus = "decomposed" // split into children (terminal; work moved to children)
)

// IsTerminal reports whether the status is an end state.
func (s TicketStatus) IsTerminal() bool {
	return s == StatusMerged || s == StatusFailed || s == StatusDecomposed
}

// Goal is a submitted objective.
type Goal struct {
	ID             string
	Text           string
	Amendments     []string       // steering guidance appended mid-run (GoalAmended)
	TokensSpent    int            // LLM tokens charged to this Goal's tickets (spend governor)
	TokensByModel  map[string]int // LLM tokens charged to this Goal, broken down by model
	BudgetExceeded bool           // the per-Goal token/USD budget tripped; no more work is dispatched
}

// EffectiveText is the Goal's text plus any mid-run amendments, as handed to a
// worker for context. Amendments steer future dispatches without rewriting the
// original objective.
func (g *Goal) EffectiveText() string {
	if len(g.Amendments) == 0 {
		return g.Text
	}
	var b strings.Builder
	b.WriteString(g.Text)
	b.WriteString("\n\nAmendments (most recent guidance):")
	for _, a := range g.Amendments {
		b.WriteString("\n- ")
		b.WriteString(a)
	}
	return b.String()
}

// CostUSD computes the total USD cost of the Goal based on the provided pricing map
// (which maps model ID to USD per million tokens).
func (g *Goal) CostUSD(pricing map[string]float64) float64 {
	if len(pricing) == 0 {
		return 0
	}
	var total float64
	for model, tokens := range g.TokensByModel {
		if rate, ok := pricing[model]; ok {
			total += (float64(tokens) / 1_000_000.0) * rate
		}
	}
	return total
}

// Ticket is a unit of work in the task graph.
type Ticket struct {
	ID             string
	GoalID         string
	Title          string
	DependsOn      []string
	Children       []string // set when the ticket is decomposed into child tickets
	IdempotencyKey string
	Status         TicketStatus
	Worker         string
	Attempts       int
	Branch         string
	Commit         string
	Trace          string
	Depth          int  // decomposition depth; tickets seeded from a goal are 0
	Approved       bool // a human approved the parked proposal (ADR 008)
	LastActivity   time.Time
	LastFailReason string // reason of the most recent verification failure (crash-loop detection)
	SameFailCount  int    // consecutive verification failures sharing LastFailReason
	Worktree       string // preserved checkout of a terminally-failed attempt (warm handoff); empty otherwise
}

// State is the derived snapshot produced by folding events.
type State struct {
	Goals       map[string]*Goal
	Tickets     map[string]*Ticket
	ticketOrder []string          // creation order, for deterministic iteration
	keyToTicket map[string]string // idempotency key -> ticket ID (dedupe)
	LastSeq     int
}

// New returns an empty State.
func New() *State {
	return &State{
		Goals:       map[string]*Goal{},
		Tickets:     map[string]*Ticket{},
		keyToTicket: map[string]string{},
	}
}

// Fold builds State from a slice of events in order.
func Fold(events []api.Event) (*State, error) {
	s := New()
	for _, e := range events {
		if err := s.Apply(e); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Apply folds a single event into the state.
func (s *State) Apply(e api.Event) error {
	switch e.Type {
	case api.GoalSubmitted:
		var p api.GoalSubmittedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		s.Goals[p.GoalID] = &Goal{ID: p.GoalID, Text: p.Text, TokensByModel: map[string]int{}}

	case api.TicketCreated:
		var p api.TicketCreatedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		// Idempotency: a duplicate logical ticket is a no-op (ADR 001).
		if p.IdempotencyKey != "" {
			if _, seen := s.keyToTicket[p.IdempotencyKey]; seen {
				break
			}
			s.keyToTicket[p.IdempotencyKey] = p.TicketID
		}
		if _, exists := s.Tickets[p.TicketID]; exists {
			break
		}
		s.Tickets[p.TicketID] = &Ticket{
			ID:             p.TicketID,
			GoalID:         p.GoalID,
			Title:          p.Title,
			DependsOn:      p.DependsOn,
			IdempotencyKey: p.IdempotencyKey,
			Depth:          p.Depth,
			Status:         StatusPending,
			LastActivity:   e.Timestamp,
		}
		s.ticketOrder = append(s.ticketOrder, p.TicketID)

	case api.TicketDecomposed:
		var p api.TicketDecomposedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		// The parent's work has moved to its children; mark it terminal.
		if t := s.Tickets[p.TicketID]; t != nil {
			t.Status = StatusDecomposed
			t.Children = p.Children
			t.LastActivity = e.Timestamp
			if g := s.Goals[t.GoalID]; g != nil {
				g.TokensSpent += p.Tokens
				if p.Model != "" {
					g.TokensByModel[p.Model] += p.Tokens
				}
			}
		}

	case api.TicketReady:
		var p api.TicketReadyPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		if t := s.Tickets[p.TicketID]; t != nil && t.Status == StatusPending {
			t.Status = StatusReady
		}

	case api.TicketClaimed:
		var p api.TicketClaimedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		if t := s.Tickets[p.TicketID]; t != nil {
			t.Status = StatusClaimed
			t.Worker = p.Worker
			t.Attempts++
			t.LastActivity = e.Timestamp
		}

	case api.WorkStarted:
		var p api.WorkStartedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		if t := s.Tickets[p.TicketID]; t != nil {
			t.Status = StatusRunning
			t.LastActivity = e.Timestamp
		}

	case api.Heartbeat:
		var p api.HeartbeatPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		if t := s.Tickets[p.TicketID]; t != nil {
			t.LastActivity = e.Timestamp
		}

	case api.ProposalSubmitted:
		var p api.ProposalSubmittedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		if t := s.Tickets[p.TicketID]; t != nil {
			t.Status = StatusProposed
			t.Branch = p.Branch
			t.Commit = p.Commit
			t.Trace = p.Trace
			t.LastActivity = e.Timestamp
			if g := s.Goals[t.GoalID]; g != nil {
				g.TokensSpent += p.Tokens
				if p.Model != "" {
					g.TokensByModel[p.Model] += p.Tokens
				}
			}
		}

	case api.VerificationPassed:
		// No status change here; the merge step (Merged) is the transition.
		// Recorded for the audit trail.

	case api.VerificationFailed:
		var p api.VerificationFailedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		// Send the ticket back to Ready for another attempt; the reconciler
		// caps attempts and emits TicketFailed when exhausted.
		if t := s.Tickets[p.TicketID]; t != nil {
			// Track the consecutive identical-reason failure streak so the
			// reconciler can detect a crash loop (same failure, no progress).
			if p.Reason != "" && p.Reason == t.LastFailReason {
				t.SameFailCount++
			} else {
				t.LastFailReason = p.Reason
				t.SameFailCount = 1
			}
			t.Status = StatusReady
			t.Worker = ""
			t.Branch, t.Commit, t.Trace = "", "", ""
			t.LastActivity = e.Timestamp
		}

	case api.Merged:
		var p api.MergedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		if t := s.Tickets[p.TicketID]; t != nil {
			t.Status = StatusMerged
			t.Commit = p.Commit
			t.LastActivity = e.Timestamp
		}

	case api.TicketFailed:
		var p api.TicketFailedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		if t := s.Tickets[p.TicketID]; t != nil {
			t.Status = StatusFailed
			t.LastActivity = e.Timestamp
			if p.Reason != "" {
				t.LastFailReason = p.Reason
			}
			t.Worktree = p.Worktree // preserved checkout for a warm handoff ("" if none)
		}

	case api.ApprovalRequested:
		var p api.ApprovalRequestedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		// A verified proposal parks for a human decision.
		if t := s.Tickets[p.TicketID]; t != nil && t.Status == StatusProposed {
			t.Status = StatusAwaiting
			t.LastActivity = e.Timestamp
		}

	case api.ApprovalGranted:
		var p api.ApprovalGrantedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		// Approval returns the ticket to the merge queue, now flagged approved so
		// the queue performs the real verify+merge instead of another dry run.
		if t := s.Tickets[p.TicketID]; t != nil && t.Status == StatusAwaiting {
			t.Status = StatusProposed
			t.Approved = true
			t.LastActivity = e.Timestamp
		}

	case api.ApprovalDenied:
		var p api.ApprovalDeniedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		if t := s.Tickets[p.TicketID]; t != nil && t.Status == StatusAwaiting {
			t.Status = StatusFailed
			t.LastActivity = e.Timestamp
		}

	case api.GoalBudgetExceeded:
		var p api.GoalBudgetExceededPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		if g := s.Goals[p.GoalID]; g != nil {
			g.BudgetExceeded = true
		}

	case api.GoalAmended:
		var p api.GoalAmendedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		if g := s.Goals[p.GoalID]; g != nil && p.Guidance != "" {
			g.Amendments = append(g.Amendments, p.Guidance)
		}

	case api.RegressionEscaped:
		// Observational only: the broader Shadow set failed on a commit the Gate
		// accepted. The merge stands; this feeds the regression-escape-rate
		// metric. No state transition, but it must replay cleanly (total Apply).

	case api.WorkerStalled:
		// Advisory; the WorkerRestarted event performs the reset.

	case api.WorkerRestarted:
		var p api.WorkerRestartedPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		if t := s.Tickets[p.TicketID]; t != nil {
			t.Status = StatusReady
			t.Worker = ""
			t.Branch, t.Commit, t.Trace = "", "", ""
			t.LastActivity = e.Timestamp
		}

	default:
		return fmt.Errorf("state: unknown event type %q (seq %d)", e.Type, e.Seq)
	}

	s.LastSeq = e.Seq
	return nil
}

// --- Queries --------------------------------------------------------------

// orderedTickets returns tickets in creation order.
func (s *State) orderedTickets() []*Ticket {
	out := make([]*Ticket, 0, len(s.ticketOrder))
	for _, id := range s.ticketOrder {
		if t := s.Tickets[id]; t != nil {
			out = append(out, t)
		}
	}
	return out
}

// DepsSatisfied reports whether every dependency of the ticket is complete.
func (s *State) DepsSatisfied(t *Ticket) bool {
	for _, dep := range t.DependsOn {
		if !s.ticketComplete(dep) {
			return false
		}
	}
	return true
}

// ticketComplete reports whether a dependency is fully done: merged directly,
// or decomposed with every descendant complete. The graph is a DAG (enforced
// at creation), so the recursion terminates.
func (s *State) ticketComplete(id string) bool {
	d := s.Tickets[id]
	if d == nil {
		return false
	}
	switch d.Status {
	case StatusMerged:
		return true
	case StatusDecomposed:
		for _, c := range d.Children {
			if !s.ticketComplete(c) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// ticketDead reports whether a dependency can never complete: it has terminally
// failed, or it decomposed into a subtree containing a dead descendant. An
// unknown id is not provably dead (it may still be created by emergent work).
func (s *State) ticketDead(id string) bool {
	d := s.Tickets[id]
	if d == nil {
		return false
	}
	switch d.Status {
	case StatusFailed:
		return true
	case StatusDecomposed:
		for _, c := range d.Children {
			if s.ticketDead(c) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// DeadDependency returns the id of a dependency that can never complete, if any.
func (s *State) DeadDependency(t *Ticket) string {
	for _, dep := range t.DependsOn {
		if s.ticketDead(dep) {
			return dep
		}
	}
	return ""
}

// Blocked returns non-terminal tickets that can never become ready because a
// dependency has terminally failed. The reconciler fails these to preserve
// liveness — no ticket waits forever on dead work. Order is deterministic.
func (s *State) Blocked() []*Ticket {
	var out []*Ticket
	for _, t := range s.orderedTickets() {
		if t.Status.IsTerminal() {
			continue
		}
		if s.DeadDependency(t) != "" {
			out = append(out, t)
		}
	}
	return out
}

// TicketForKey returns the ticket ID an idempotency key currently maps to, if
// any. The Scheduler uses it so an emergent child whose key already names a
// ticket is adopted rather than re-created (which state.Apply would dedupe to a
// no-op, leaving the parent waiting on a ticket that never appears).
func (s *State) TicketForKey(key string) (string, bool) {
	id, ok := s.keyToTicket[key]
	return id, ok
}

// WouldCycle reports whether adding the given edges (ticket id -> dependency
// ids) to the current dependency graph would introduce a cycle. The Scheduler
// uses this to reject emergent decompositions that would deadlock the graph.
func (s *State) WouldCycle(extra map[string][]string) bool {
	adj := make(map[string][]string, len(s.Tickets))
	for id, t := range s.Tickets {
		adj[id] = t.DependsOn
	}
	for n, deps := range extra {
		adj[n] = append(append([]string(nil), adj[n]...), deps...)
	}
	return HasCycle(adj)
}

// HasCycle reports whether the directed graph adj (node -> out-neighbors) has a
// cycle, via a three-color depth-first search. Nodes referenced only as
// neighbors are treated as having no out-edges.
func HasCycle(adj map[string][]string) bool {
	const (
		white = 0 // unvisited
		gray  = 1 // on the current DFS stack
		black = 2 // fully explored
	)
	color := make(map[string]int, len(adj))
	var visit func(n string) bool
	visit = func(n string) bool {
		color[n] = gray
		for _, m := range adj[n] {
			switch color[m] {
			case gray:
				return true
			case white:
				if visit(m) {
					return true
				}
			}
		}
		color[n] = black
		return false
	}
	for n := range adj {
		if color[n] == white && visit(n) {
			return true
		}
	}
	return false
}

// NewlyReady returns pending tickets whose dependencies are now satisfied. The
// reconciler emits TicketReady for these. Order is deterministic (creation order).
func (s *State) NewlyReady() []*Ticket {
	var out []*Ticket
	for _, t := range s.orderedTickets() {
		if t.Status == StatusPending && s.DepsSatisfied(t) {
			out = append(out, t)
		}
	}
	return out
}

// ReadyTickets returns tickets ready to dispatch, in creation order.
func (s *State) ReadyTickets() []*Ticket {
	var out []*Ticket
	for _, t := range s.orderedTickets() {
		if t.Status == StatusReady {
			out = append(out, t)
		}
	}
	return out
}

// Proposed returns tickets awaiting the merge queue, in creation order.
func (s *State) Proposed() []*Ticket {
	var out []*Ticket
	for _, t := range s.orderedTickets() {
		if t.Status == StatusProposed {
			out = append(out, t)
		}
	}
	return out
}

// AwaitingApproval returns tickets parked for a human decision, in creation
// order. The Scheduler uses this to pause cleanly rather than spin when the
// only remaining work needs human approval (ADR 008).
func (s *State) AwaitingApproval() []*Ticket {
	var out []*Ticket
	for _, t := range s.orderedTickets() {
		if t.Status == StatusAwaiting {
			out = append(out, t)
		}
	}
	return out
}

// ActiveCount returns the number of tickets occupying a worker slot
// (claimed or running). Used by the concurrency governor as backpressure.
func (s *State) ActiveCount() int {
	n := 0
	for _, t := range s.Tickets {
		if t.Status == StatusClaimed || t.Status == StatusRunning {
			n++
		}
	}
	return n
}

// Settled reports whether no ticket remains in a non-terminal state. With at
// least one ticket present, this means the run is complete.
func (s *State) Settled() bool {
	for _, t := range s.Tickets {
		if !t.Status.IsTerminal() {
			return false
		}
	}
	return true
}
