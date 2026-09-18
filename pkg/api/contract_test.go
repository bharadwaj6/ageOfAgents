package api

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestContractWireShape pins the --json output of the write verbs to golden
// files. Front doors parse these bytes: a renamed field, a changed tag or a
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
