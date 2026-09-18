package api

// ContractVersion is the version of the CLI's machine-readable output: the
// result types below, as the write verbs print them with --json. Every result
// carries it in its "schema" field so a caller can tell which shape it is
// reading.
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
