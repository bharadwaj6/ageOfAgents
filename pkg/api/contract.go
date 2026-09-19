package api

import "time"

// ContractVersion is the version of the CLI's machine-readable output: the
// result types below, as the write verbs and `aoa status` print them with
// --json. Every result carries it in its "schema" field so a caller can tell
// which shape it is reading.
//
// Within a version, fields are only ever added — a caller must ignore fields it
// does not know. Renaming or removing a field, or changing what one means,
// bumps ContractVersion.
const ContractVersion = 1

// Decision values reported in [DecisionResult].
const (
	DecisionApproved = "approved"
	DecisionRejected = "rejected"
)

// SubmitResult is what `aoa goal --json` prints. Submitting with an idempotency
// key that is already on the log appends nothing: Duplicate is true, GoalID is
// the Goal the key already names, and Seq is the sequence number of that Goal's
// original GoalSubmitted event. Otherwise Seq is the event just appended.
type SubmitResult struct {
	Schema    int    `json:"schema"`
	GoalID    string `json:"goal_id"`
	Duplicate bool   `json:"duplicate"`
	Seq       int    `json:"seq"`
}

// AmendResult is what `aoa amend --json` prints. Seq is the sequence number of
// the GoalAmended event appended.
type AmendResult struct {
	Schema int    `json:"schema"`
	GoalID string `json:"goal_id"`
	Seq    int    `json:"seq"`
}

// DecisionResult is what `aoa approve --json` and `aoa reject --json` print.
// Decision is [DecisionApproved] or [DecisionRejected]. Repeating a decision
// already on the log appends nothing: AlreadyDecided is true and Seq is the
// sequence number of the original decision. Otherwise Seq is the event just
// appended.
type DecisionResult struct {
	Schema         int    `json:"schema"`
	TicketID       string `json:"ticket_id"`
	Decision       string `json:"decision"`
	Seq            int    `json:"seq"`
	AlreadyDecided bool   `json:"already_decided"`
}

// Goal outcomes reported in [GoalView]. A Goal is queued until the Scheduler
// has created its first task, running while any of its tasks is still in
// flight, and awaiting_approval while any of them is parked for a human. Once
// none of its tasks can move without new input it is merged, when all of its
// work landed, or failed, when any of it did not.
//
// Failed includes partial success: a Goal decomposed into several tasks, some
// merged and some failed, is failed — the work it asked for is not all there —
// and the commits that did merge are still listed in [GoalView].Commits. A task
// a human rejected is a failed task, and a Goal whose token or cost budget
// tripped is failed once it settles, whatever merged before the trip.
const (
	OutcomeQueued           = "queued"
	OutcomeRunning          = "running"
	OutcomeAwaitingApproval = "awaiting_approval"
	OutcomeMerged           = "merged"
	OutcomeFailed           = "failed"
)

// StatusView is what `aoa status --json` prints: a snapshot of every Goal on
// the Event Log, where it came from, and its outcome, as one JSON value.
//
// LastSeq is the seq of the last event the snapshot folds. A caller that wants
// to keep the snapshot current can resume from it with
// `aoa events --json --since <last_seq> --follow`.
//
// Settled is true when nothing more will happen without new input: every Goal
// is merged or failed. A queued Goal or a task awaiting approval is not
// settled. An empty workspace is settled, with an empty Goals list.
//
// Goals are listed in submission order (the seq of their GoalSubmitted event).
type StatusView struct {
	Schema     int            `json:"schema"`
	LastSeq    int            `json:"last_seq"`
	Settled    bool           `json:"settled"`
	Goals      []GoalView     `json:"goals"`
	Totals     StatusTotals   `json:"totals"`
	MergeQueue MergeQueueView `json:"merge_queue"`
}

// GoalView is one Goal in a [StatusView].
//
// Source, Ref and By are the origin the submitter recorded (see
// GoalSubmittedPayload); Ref and By are omitted when none was given. Outcome is
// one of the Outcome constants. BudgetExceeded is true once the Goal's token or
// cost budget has tripped; no new work is dispatched for it after that.
//
// Tokens is every token its tasks consumed, failed and retried attempts
// included, and CostUSD prices them with the workspace's pricing table (0 when
// a model is unpriced). Amendments is the steering guidance appended to the
// Goal, oldest first. Commits lists the merged commits of its tasks, in task
// order — present on a failed Goal too, when part of its work merged.
//
// Tickets lists the Goal's tasks in creation order, so a decomposed task comes
// before its children. It is empty, not absent, for a queued Goal.
type GoalView struct {
	ID             string       `json:"id"`
	Text           string       `json:"text"`
	Source         string       `json:"source"`
	Ref            string       `json:"ref,omitempty"`
	By             string       `json:"by,omitempty"`
	SubmittedAt    time.Time    `json:"submitted_at"`
	Outcome        string       `json:"outcome"`
	BudgetExceeded bool         `json:"budget_exceeded"`
	Tokens         int          `json:"tokens"`
	CostUSD        float64      `json:"cost_usd"`
	Amendments     []string     `json:"amendments,omitempty"`
	Commits        []string     `json:"commits,omitempty"`
	Graph          GraphView    `json:"graph"`
	Tickets        []TicketView `json:"tickets"`
}

// GraphView is the shape of a Goal's task graph: MaxDepth is the deepest
// decomposition level reached (0 when nothing was decomposed) and MaxFanOut the
// most children any one task was split into.
type GraphView struct {
	MaxDepth  int `json:"max_depth"`
	MaxFanOut int `json:"max_fan_out"`
}

// TicketView is one task in a [GoalView].
//
// Status is the task's lifecycle stage: pending, ready, claimed, running,
// proposed, awaiting (parked for approval), merged, failed or decomposed (split
// into child tasks, which carry the work). Attempts counts dispatches and
// Tokens every token they consumed. Depth is the decomposition level; tasks
// created from the Goal itself are 0.
//
// Commit is the merged commit when Status is merged, and the candidate commit
// under verification or review when Status is proposed or awaiting; it is
// omitted otherwise. FailReason and Worktree are set only when Status is
// failed: why, and the preserved checkout of the last attempt for a human to
// take over (omitted when none was kept). Rejected is true when the failure was
// a human rejecting the proposal.
type TicketView struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Attempts   int    `json:"attempts"`
	Tokens     int    `json:"tokens"`
	Depth      int    `json:"depth"`
	Commit     string `json:"commit,omitempty"`
	FailReason string `json:"fail_reason,omitempty"`
	Worktree   string `json:"worktree,omitempty"`
	Rejected   bool   `json:"rejected,omitempty"`
}

// StatusTotals sums a [StatusView] across every Goal. Tokens and CostUSD are
// the run's whole spend, priced as in [GoalView]. WallSeconds is the time from
// the first event on the log to the last. Goals and Tickets count what exists;
// Merged, Failed and Awaiting count tasks in those states.
type StatusTotals struct {
	Tokens      int     `json:"tokens"`
	CostUSD     float64 `json:"cost_usd"`
	WallSeconds float64 `json:"wall_seconds"`
	Goals       int     `json:"goals"`
	Tickets     int     `json:"tickets"`
	Merged      int     `json:"merged"`
	Failed      int     `json:"failed"`
	Awaiting    int     `json:"awaiting"`
}

// MergeQueueView is how the merge queue behaved over the whole log: the most
// proposals waiting at once, and the mean and worst time a proposal waited for
// its verdict, in seconds. All zero when nothing was ever proposed.
type MergeQueueView struct {
	MaxDepth        int     `json:"max_depth"`
	WaitMeanSeconds float64 `json:"wait_mean_seconds"`
	WaitMaxSeconds  float64 `json:"wait_max_seconds"`
}
