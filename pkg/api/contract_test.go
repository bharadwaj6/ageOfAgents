package api

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestContractWireShape pins the --json output of the write verbs and of
// `aoa status` to golden files. Front doors parse these bytes: a renamed field, a changed tag or a
// dropped omitempty here is a breaking change, and must fail a test rather than
// ship. Values are fully populated so every field appears on the wire. If this
// fails because a field was deliberately renamed or removed, ContractVersion
// must be bumped too (see its doc comment).
func TestContractWireShape(t *testing.T) {
	cases := []struct {
		name   string
		value  any
		golden string
	}{
		{
			name:   "SubmitResult",
			value:  SubmitResult{Schema: ContractVersion, GoalID: "g-1a2b3c4d", Duplicate: true, Seq: 7},
			golden: "submit_result.json",
		},
		{
			name:   "AmendResult",
			value:  AmendResult{Schema: ContractVersion, GoalID: "g-1a2b3c4d", Seq: 12},
			golden: "amend_result.json",
		},
		{
			name: "DecisionResult",
			value: DecisionResult{
				Schema: ContractVersion, TicketID: "g-1a2b3c4d-impl", Decision: DecisionApproved,
				Seq: 20, AlreadyDecided: true,
			},
			golden: "decision_result.json",
		},
		{
			name:   "CancelResult",
			value:  CancelResult{Schema: ContractVersion, GoalID: "g-1a2b3c4d", Seq: 30, AlreadyCancelled: true},
			golden: "cancel_result.json",
		},
		{
			name: "StatusView",
			value: StatusView{
				Schema: ContractVersion, LastSeq: 42, Settled: true,
				Goals: []GoalView{{
					ID: "g-1a2b3c4d", Text: "add a greeting", Source: "linear",
					Ref: "https://linear.app/x/ENG-1", By: "octocat",
					SubmittedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
					Outcome:     OutcomeFailed, BudgetExceeded: true, Tokens: 3000, CostUSD: 0.0125,
					Amendments: []string{"prefer table-driven tests"},
					Commits:    []string{"c0ffee1"},
					Branch:     "aoa/g-1a2b3c4d", PRURL: "https://github.com/o/r/pull/7",
					DeliveryError: "push aoa/g-1a2b3c4d: rejected (fetch first)",
					Graph:         GraphView{MaxDepth: 1, MaxFanOut: 2},
					Tickets: []TicketView{
						{ID: "g-1a2b3c4d-impl/a", Title: "write the lexer", Status: "merged", Attempts: 1, Tokens: 1000, Depth: 1, Commit: "c0ffee1"},
						{
							ID: "g-1a2b3c4d-impl/b", Title: "write the grammar", Status: "failed", Attempts: 2, Tokens: 2000, Depth: 1,
							FailReason: "rejected by a human", Worktree: "/ws/.aoa/handoff/g-1a2b3c4d-impl-b", Rejected: true,
						},
					},
				}},
				Totals: StatusTotals{
					Tokens: 3000, CostUSD: 0.0125, WallSeconds: 61.5,
					Goals: 1, Tickets: 3, Merged: 1, Failed: 1, Awaiting: 0,
				},
				MergeQueue: MergeQueueView{MaxDepth: 2, WaitMeanSeconds: 2.5, WaitMaxSeconds: 4},
			},
			golden: "status_view.json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Encode exactly as the CLI does: one compact line.
			var got bytes.Buffer
			if err := json.NewEncoder(&got).Encode(tc.value); err != nil {
				t.Fatalf("encode: %v", err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", tc.golden))
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if !bytes.Equal(got.Bytes(), want) {
				t.Errorf("wire shape changed:\n got  %s want %s", got.Bytes(), want)
			}
		})
	}
}
