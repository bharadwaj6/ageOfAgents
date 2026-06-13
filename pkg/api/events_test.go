package api

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNewEventRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		typ     EventType
		payload any
		decode  func() any // returns a fresh pointer to decode into
	}{
		{
			name:    "GoalSubmitted",
			typ:     GoalSubmitted,
			payload: GoalSubmittedPayload{GoalID: "g1", Text: "add greeting"},
			decode:  func() any { return &GoalSubmittedPayload{} },
		},
		{
			name:    "TicketCreated",
			typ:     TicketCreated,
			payload: TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "impl", DependsOn: []string{"t0"}, IdempotencyKey: "k1", CreatedBy: "alice"},
			decode:  func() any { return &TicketCreatedPayload{} },
		},
		{
			name:    "ProposalSubmitted",
			typ:     ProposalSubmitted,
			payload: ProposalSubmittedPayload{TicketID: "t1", Worker: "alice", Branch: "aoa/t1", Commit: "abc123", Trace: "did the thing"},
			decode:  func() any { return &ProposalSubmittedPayload{} },
		},
		{
			name:    "VerificationFailed",
			typ:     VerificationFailed,
			payload: VerificationFailedPayload{TicketID: "t1", Worker: "alice", Reason: "tests failed", Output: "FAIL"},
			decode:  func() any { return &VerificationFailedPayload{} },
		},
		{
			name:    "Merged",
			typ:     Merged,
			payload: MergedPayload{TicketID: "t1", Worker: "alice", Commit: "def456"},
			decode:  func() any { return &MergedPayload{} },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := NewEvent(tc.typ, "tester", tc.payload)
			if err != nil {
				t.Fatalf("NewEvent: %v", err)
			}
			if ev.Type != tc.typ {
				t.Errorf("type = %q, want %q", ev.Type, tc.typ)
			}
			if ev.Timestamp.IsZero() {
				t.Error("timestamp not set")
			}
			if ev.Seq != 0 {
				t.Errorf("seq = %d, want 0 (assigned by ledger)", ev.Seq)
			}

			// Marshal the whole envelope and read it back, as the ledger will.
			raw, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal event: %v", err)
			}
			var got Event
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("unmarshal event: %v", err)
			}

			want := tc.decode()
			if err := got.DecodePayload(want); err != nil {
				t.Fatalf("DecodePayload: %v", err)
			}
			// Compare the decoded payload against a freshly-decoded original.
			orig := tc.decode()
			if err := ev.DecodePayload(orig); err != nil {
				t.Fatalf("DecodePayload(original): %v", err)
			}
			if !reflect.DeepEqual(want, orig) {
				t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", want, orig)
			}
		})
	}
}

func TestNewEventNilPayload(t *testing.T) {
	ev, err := NewEvent(Heartbeat, "alice", nil)
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if len(ev.Payload) != 0 {
		t.Errorf("expected empty payload, got %s", ev.Payload)
	}
	if err := ev.DecodePayload(&HeartbeatPayload{}); err == nil {
		t.Error("expected error decoding empty payload")
	}
}

func TestDecodePayloadError(t *testing.T) {
	ev, _ := NewEvent(TicketReady, "x", TicketReadyPayload{TicketID: "t1"})
	// Decoding into an incompatible (non-pointer-struct) type should error.
	var notAStruct int
	if err := ev.DecodePayload(&notAStruct); err == nil {
		t.Error("expected decode error for incompatible target")
	}
}
