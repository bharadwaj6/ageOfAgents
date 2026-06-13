// Package api defines the public event vocabulary for the Age of Agents
// orchestrator. The append-only Event Log of these events is the single source
// of truth (see docs/v2/adr/001-event-sourced-truth.md); all runtime state is
// derived by replaying the event stream.
package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// EventType identifies the kind of an [Event]. The set is deliberately small.
type EventType string

const (
	// GoalSubmitted: a human submitted an objective to decompose.
	GoalSubmitted EventType = "GoalSubmitted"
	// TicketCreated: a unit of work was added to the graph. May be emitted by
	// the initial decomposition or by a worker at runtime (emergent graph).
	TicketCreated EventType = "TicketCreated"
	// TicketDecomposed: a worker split a ticket into child tickets (emergent
	// decomposition) instead of proposing a change. The parent is terminal; the
	// children carry the work. Coordination is via the Shared Log (ADR 006).
	TicketDecomposed EventType = "TicketDecomposed"
	// TicketReady: a ticket's dependencies are all satisfied; it is dispatchable.
	TicketReady EventType = "TicketReady"
	// TicketClaimed: a worker took ownership of a ready ticket.
	TicketClaimed EventType = "TicketClaimed"
	// WorkStarted: a worker began executing in its isolated worktree.
	WorkStarted EventType = "WorkStarted"
	// Heartbeat: a liveness signal from an active worker.
	Heartbeat EventType = "Heartbeat"
	// ProposalSubmitted: a worker produced a candidate change for the merge queue.
	ProposalSubmitted EventType = "ProposalSubmitted"
	// VerificationPassed: the objective verifier accepted a proposal.
	VerificationPassed EventType = "VerificationPassed"
	// VerificationFailed: the objective verifier rejected a proposal.
	VerificationFailed EventType = "VerificationFailed"
	// Merged: a verified proposal was merged into the integration branch.
	Merged EventType = "Merged"
	// TicketFailed: a ticket could not be completed (terminal for the attempt).
	TicketFailed EventType = "TicketFailed"
	// WorkerStalled: the failure detector flagged a worker with no progress.
	WorkerStalled EventType = "WorkerStalled"
	// WorkerRestarted: a stalled worker's ticket was reset for a fresh attempt.
	WorkerRestarted EventType = "WorkerRestarted"
)

// Event is the append-only log envelope. Seq is assigned by the ledger on
// append; callers leave it zero when constructing events.
type Event struct {
	Seq       int             `json:"seq"`
	Type      EventType       `json:"type"`
	Timestamp time.Time       `json:"ts"`
	Actor     string          `json:"actor,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// NewEvent builds an Event of the given type, marshaling payload into the
// envelope. The payload may be nil for events that carry no data.
func NewEvent(typ EventType, actor string, payload any) (Event, error) {
	e := Event{Type: typ, Timestamp: time.Now().UTC(), Actor: actor}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return Event{}, fmt.Errorf("marshal %s payload: %w", typ, err)
		}
		e.Payload = raw
	}
	return e, nil
}

// DecodePayload unmarshals the event payload into v.
func (e Event) DecodePayload(v any) error {
	if len(e.Payload) == 0 {
		return fmt.Errorf("event %s (seq %d) has no payload", e.Type, e.Seq)
	}
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("decode %s payload: %w", e.Type, err)
	}
	return nil
}

// --- Typed payloads -------------------------------------------------------

// GoalSubmittedPayload accompanies [GoalSubmitted].
type GoalSubmittedPayload struct {
	GoalID string `json:"goal_id"`
	Text   string `json:"text"`
}

// TicketCreatedPayload accompanies [TicketCreated]. IdempotencyKey makes
// re-creating the same logical ticket a no-op (see ADR 001). DependsOn lists
// ticket IDs that must complete before this ticket becomes ready.
type TicketCreatedPayload struct {
	TicketID       string   `json:"ticket_id"`
	GoalID         string   `json:"goal_id"`
	Title          string   `json:"title"`
	DependsOn      []string `json:"depends_on,omitempty"`
	IdempotencyKey string   `json:"idempotency_key"`
	CreatedBy      string   `json:"created_by,omitempty"` // worker id for emergent tickets
	Depth          int      `json:"depth,omitempty"`      // decomposition depth; root tickets are 0
}

// TicketDecomposedPayload accompanies [TicketDecomposed]. Children are the
// ticket IDs the worker created (also emitted as [TicketCreated] events); the
// parent ticket becomes terminal.
type TicketDecomposedPayload struct {
	TicketID string   `json:"ticket_id"`
	Worker   string   `json:"worker,omitempty"`
	Children []string `json:"children"`
}

// TicketReadyPayload accompanies [TicketReady].
type TicketReadyPayload struct {
	TicketID string `json:"ticket_id"`
}

// TicketClaimedPayload accompanies [TicketClaimed].
type TicketClaimedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker"`
}

// WorkStartedPayload accompanies [WorkStarted].
type WorkStartedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker"`
	Worktree string `json:"worktree"`
}

// HeartbeatPayload accompanies [Heartbeat].
type HeartbeatPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker"`
}

// ProposalSubmittedPayload accompanies [ProposalSubmitted].
type ProposalSubmittedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker"`
	Branch   string `json:"branch"`
	Commit   string `json:"commit"`
	Trace    string `json:"trace,omitempty"`
}

// VerificationPassedPayload accompanies [VerificationPassed].
type VerificationPassedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker"`
}

// VerificationFailedPayload accompanies [VerificationFailed].
type VerificationFailedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker"`
	Reason   string `json:"reason"`
	Output   string `json:"output,omitempty"`
}

// MergedPayload accompanies [Merged].
type MergedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker"`
	Commit   string `json:"commit"`
}

// TicketFailedPayload accompanies [TicketFailed].
type TicketFailedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker,omitempty"`
	Reason   string `json:"reason"`
}

// WorkerStalledPayload accompanies [WorkerStalled].
type WorkerStalledPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker"`
}

// WorkerRestartedPayload accompanies [WorkerRestarted].
type WorkerRestartedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker"`
}
