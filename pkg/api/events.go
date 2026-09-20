// Package api is the public contract of the Age of Agents orchestrator. It
// holds two things external callers depend on:
//
//   - the event vocabulary: the append-only Event Log of these events is the
//     single source of truth (see docs/design/adr/001-event-sourced-truth.md),
//     and all runtime state is derived by replaying the event stream;
//   - the CLI's machine-readable contract: the result types the write verbs
//     print with --json (contract.go), versioned by [ContractVersion].
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
	// ApprovalRequested: a proposal passed the Gate (dry-run) and is parked for a
	// human decision before it may merge (optional human-in-the-loop gate, ADR 008).
	ApprovalRequested EventType = "ApprovalRequested"
	// ApprovalGranted: a human approved a parked proposal; it may now merge.
	ApprovalGranted EventType = "ApprovalGranted"
	// ApprovalDenied: a human rejected a parked proposal; the ticket fails.
	ApprovalDenied EventType = "ApprovalDenied"
	// GoalBudgetExceeded: a Goal reached its per-Goal token budget; the spend
	// governor stopped dispatching its remaining work (circuit breaker).
	GoalBudgetExceeded EventType = "GoalBudgetExceeded"
	// RegressionEscaped: a merge passed the Gate but a broader Shadow test set
	// failed on it — a verification blind spot. Observational (the merge stands);
	// it feeds the regression-escape-rate metric.
	RegressionEscaped EventType = "RegressionEscaped"
	// GoalAmended: a human appended steering guidance to a Goal mid-run. Future
	// dispatches (and retries) carry the amended context; running workers are not
	// preempted. Feeds the stale_spec_drift diagnose signal.
	GoalAmended EventType = "GoalAmended"
	// GoalCancelled: a human (or the front door acting for one) withdrew a Goal.
	// None of its work may land after this: the Scheduler dispatches nothing more
	// for it and fails every task of it that is not in flight, parked proposals
	// included; an attempt already running finishes and its proposal is failed.
	GoalCancelled EventType = "GoalCancelled"
	// Delivered: in pull-request delivery mode (ADR 016), a complete Goal's
	// branch was pushed and its pull request opened. Recorded once per Goal.
	Delivered EventType = "Delivered"
	// DeliveryFailed: pushing a complete Goal's branch or opening its pull
	// request failed. The Goal stays pending delivery; the next run retries.
	DeliveryFailed EventType = "DeliveryFailed"
	// StateSnapshot: a compaction event containing the full derived state.
	// Used to bootstrap state without replaying the entire history.
	StateSnapshot EventType = "StateSnapshot"
	// TicketInvalidated: a worker detected that a ticket's upstream dependencies or
	// assumptions have changed, making its current state invalid. The DAG should
	// re-evaluate its readiness.
	TicketInvalidated EventType = "TicketInvalidated"
	// TicketAmended: a worker updated a ticket's parameters or instructions dynamically,
	// usually because of discoveries during execution.
	TicketAmended EventType = "TicketAmended"
	// BudgetExhausted: a run or day budget held work back (ADR 017). Past a
	// spend limit the Scheduler dispatches no new attempt and starts no new
	// Goal; past a Goal limit it starts no new Goal. Attempts already running
	// finish. Recorded once per scope per window: once per run, once per UTC day.
	BudgetExhausted EventType = "BudgetExhausted"
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

// TicketID extracts the ticket_id field from any payload that carries one.
// Returns the empty string when the payload is absent or has no ticket_id.
func (e Event) TicketID() string {
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

// --- Typed payloads -------------------------------------------------------

// GoalSubmittedPayload accompanies [GoalSubmitted]. Source names where the Goal
// came from ("human" when typed at the CLI, otherwise the entry point that
// produced it). IdempotencyKey, when set, makes re-submitting the same logical
// Goal a no-op — an at-least-once source such as a redelivered webhook can
// safely replay without forking a second Goal (ADR 010). Ref and By record the
// Goal's origin for whoever reports back on it.
type GoalSubmittedPayload struct {
	GoalID         string `json:"goal_id"`
	Text           string `json:"text"`
	Source         string `json:"source,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// Ref points at the Goal's origin: a URL (an issue, a PR comment) or a
	// tracker reference such as "linear:ENG-123". Empty when there is none.
	Ref string `json:"ref,omitempty"`
	// By names who submitted the Goal (a username, a bot), as the submitter
	// reported it. Empty when unknown; aoa never guesses an identity.
	By string `json:"by,omitempty"`
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
	Tokens   int      `json:"tokens,omitempty"`   // LLM tokens the decomposition consumed (0 when unknown)
	Model    string   `json:"model,omitempty"`    // model that produced the decomposition, for per-model cost
	CostUSD  float64  `json:"cost_usd,omitempty"` // cost the harness reported for it (0 when it reports none)
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
	TicketID string  `json:"ticket_id"`
	Worker   string  `json:"worker"`
	Branch   string  `json:"branch"`
	Commit   string  `json:"commit"`
	Summary  string  `json:"summary,omitempty"` // one-line description of the change, for dependents' context
	Trace    string  `json:"trace,omitempty"`
	Tokens   int     `json:"tokens,omitempty"`   // LLM tokens the work consumed (0 when unknown)
	Model    string  `json:"model,omitempty"`    // model that produced the change, for per-model cost
	CostUSD  float64 `json:"cost_usd,omitempty"` // cost the harness reported for the work (0 when it reports none)
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

// MergedPayload accompanies [Merged]. Branch is the Goal branch the proposal
// merged into in pull-request delivery mode (ADR 016); it is empty in local
// mode, where the merge lands on the adopted repository's checked-out branch.
type MergedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker"`
	Commit   string `json:"commit"`
	Branch   string `json:"branch,omitempty"`
}

// TicketFailedPayload accompanies [TicketFailed]. Worktree, when set, is the
// preserved checkout of the agent's last attempt — kept on disk for a human to
// inspect or take over (warm handoff on terminal failure). Tokens/Model charge
// the failed attempt's spend to the Goal so the governor sees budget burned by
// work that never reached a proposal.
type TicketFailedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker,omitempty"`
	Reason   string `json:"reason"`
	// Output is the Gate's combined output when a verification failure was what
	// ended the ticket. Without it a terminal failure records only which command
	// failed, so an infrastructure fault and a genuinely broken patch are
	// indistinguishable after the fact.
	Output   string  `json:"output,omitempty"`
	Worktree string  `json:"worktree,omitempty"`
	Tokens   int     `json:"tokens,omitempty"`   // LLM tokens the failed attempt consumed (0 when unknown)
	Model    string  `json:"model,omitempty"`    // model that consumed them, for per-model cost
	CostUSD  float64 `json:"cost_usd,omitempty"` // cost the harness reported for the attempt (0 when it reports none)
}

// WorkerStalledPayload accompanies [WorkerStalled].
type WorkerStalledPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker"`
}

// WorkerRestartedPayload accompanies [WorkerRestarted]. Tokens/Model charge a
// retried attempt's spend to the Goal; a restart driven by the Stall Detector
// (rather than a failed attempt) leaves them zero.
type WorkerRestartedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker"`
	// Reason is why the attempt was abandoned (agent error, no changes, commit
	// failure, timeout). Without it a retried attempt recorded nothing about its
	// own failure: the next attempt re-ran an identical prompt, and crash-loop
	// detection — which keys on repeated identical reasons — could never fire on
	// anything but a Gate rejection.
	Reason  string  `json:"reason,omitempty"`
	Tokens  int     `json:"tokens,omitempty"`   // LLM tokens the abandoned attempt consumed (0 when unknown)
	Model   string  `json:"model,omitempty"`    // model that consumed them, for per-model cost
	CostUSD float64 `json:"cost_usd,omitempty"` // cost the harness reported for the attempt (0 when it reports none)
}

// ApprovalRequestedPayload accompanies [ApprovalRequested]. Commit is the
// dry-run merge commit the human is being asked to approve (informational).
type ApprovalRequestedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker,omitempty"`
	Commit   string `json:"commit,omitempty"`
}

// ApprovalGrantedPayload accompanies [ApprovalGranted].
type ApprovalGrantedPayload struct {
	TicketID string `json:"ticket_id"`
	By       string `json:"by,omitempty"`     // who approved (e.g. a username)
	Reason   string `json:"reason,omitempty"` // why, when the approver said
}

// ApprovalDeniedPayload accompanies [ApprovalDenied].
type ApprovalDeniedPayload struct {
	TicketID string `json:"ticket_id"`
	By       string `json:"by,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// GoalBudgetExceededPayload accompanies [GoalBudgetExceeded]. SpentTokens is the
// Goal's cumulative token spend at the moment the budget tripped. A Goal may trip
// on either ceiling: Limit is the token ceiling (0 when the trip was on cost),
// LimitUSD the dollar ceiling (0 when the trip was on tokens). SpentUSD is the
// priced spend at the trip, and is 0 when no [pricing] table covers the models used.
type GoalBudgetExceededPayload struct {
	GoalID      string  `json:"goal_id"`
	SpentTokens int     `json:"spent_tokens"`
	Limit       int     `json:"limit"`
	SpentUSD    float64 `json:"spent_usd,omitempty"`
	LimitUSD    float64 `json:"limit_usd,omitempty"`
}

// Budget scopes reported in [BudgetExhaustedPayload].
const (
	BudgetScopeRun = "run" // one `aoa run`: its --max-usd, --max-tokens, --max-goals
	BudgetScopeDay = "day" // one UTC day in the workspace: [budget] usd_per_day, tokens_per_day, goals_per_day
)

// BudgetExhaustedPayload accompanies [BudgetExhausted]. Scope is
// [BudgetScopeRun] or [BudgetScopeDay], and Day the UTC day (YYYY-MM-DD) a day
// budget covers. The Spent fields and Goals are the window's spend and Goal
// starts when it tripped; a Limit is 0 when that limit is not set.
type BudgetExhaustedPayload struct {
	Scope       string  `json:"scope"`
	Day         string  `json:"day,omitempty"`
	SpentUSD    float64 `json:"spent_usd"`
	LimitUSD    float64 `json:"limit_usd,omitempty"`
	SpentTokens int     `json:"spent_tokens"`
	LimitTokens int     `json:"limit_tokens,omitempty"`
	Goals       int     `json:"goals"`
	LimitGoals  int     `json:"limit_goals,omitempty"`
}

// GoalAmendedPayload accompanies [GoalAmended]. Guidance is steering text
// appended to the Goal's effective context for subsequent dispatches.
type GoalAmendedPayload struct {
	GoalID   string `json:"goal_id"`
	Guidance string `json:"guidance"`
}

// GoalCancelledPayload accompanies [GoalCancelled]. By and Reason are recorded
// as the canceller gave them (who withdrew the Goal, and why — "issue closed").
type GoalCancelledPayload struct {
	GoalID string `json:"goal_id"`
	By     string `json:"by,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// DeliveredPayload accompanies [Delivered]. Commit is the Goal branch's tip
// that was pushed, and URL the pull request the opener reported (empty when
// no opener is configured and delivery is push only).
type DeliveredPayload struct {
	GoalID string `json:"goal_id"`
	Branch string `json:"branch"`
	Commit string `json:"commit"`
	URL    string `json:"url,omitempty"`
}

// DeliveryFailedPayload accompanies [DeliveryFailed]. Reason says what failed
// — the push or the opener — with the tail of the command's error output.
type DeliveryFailedPayload struct {
	GoalID string `json:"goal_id"`
	Branch string `json:"branch"`
	Reason string `json:"reason"`
}

// RegressionEscapedPayload accompanies [RegressionEscaped]. Reason is what the
// broader Shadow test set reported on the merge the Gate accepted.
type RegressionEscapedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// StateSnapshotPayload accompanies [StateSnapshot].
type StateSnapshotPayload struct {
	State json.RawMessage `json:"state"`
}

// TicketInvalidatedPayload accompanies [TicketInvalidated].
type TicketInvalidatedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// TicketAmendedPayload accompanies [TicketAmended].
type TicketAmendedPayload struct {
	TicketID string `json:"ticket_id"`
	Worker   string `json:"worker,omitempty"`
	Title    string `json:"title,omitempty"` // New title if amended
	Guidance string `json:"guidance,omitempty"`
}
