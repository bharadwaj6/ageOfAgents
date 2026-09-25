package api

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
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
			name: "GoalSubmitted with origin",
			typ:  GoalSubmitted,
			payload: GoalSubmittedPayload{
				GoalID: "g2", Text: "fix the flaky test", Source: "linear", IdempotencyKey: "linear:ENG-1",
				Ref: "https://linear.app/acme/issue/ENG-1", By: "octocat",
			},
			decode: func() any { return &GoalSubmittedPayload{} },
		},
		{
			name:    "ApprovalGranted",
			typ:     ApprovalGranted,
			payload: ApprovalGrantedPayload{TicketID: "t1", By: "octocat", Reason: "reviewed the diff"},
			decode:  func() any { return &ApprovalGrantedPayload{} },
		},
		{
			name:    "GoalCancelled",
			typ:     GoalCancelled,
			payload: GoalCancelledPayload{GoalID: "g1", By: "linear-bot", Reason: "issue closed"},
			decode:  func() any { return &GoalCancelledPayload{} },
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
		{
			name:    "Merged onto a Goal branch",
			typ:     Merged,
			payload: MergedPayload{TicketID: "t1", Worker: "alice", Commit: "def456", Branch: "aoa/g1"},
			decode:  func() any { return &MergedPayload{} },
		},
		{
			name:    "Delivered",
			typ:     Delivered,
			payload: DeliveredPayload{GoalID: "g1", Branch: "aoa/g1", Commit: "def456", URL: "https://github.com/o/r/pull/7"},
			decode:  func() any { return &DeliveredPayload{} },
		},
		{
			name:    "BudgetExhausted",
			typ:     BudgetExhausted,
			payload: BudgetExhaustedPayload{Scope: BudgetScopeDay, Day: "2026-09-20", SpentUSD: 1.2, LimitUSD: 1, SpentTokens: 900, Goals: 3, LimitGoals: 5},
			decode:  func() any { return &BudgetExhaustedPayload{} },
		},
		{
			name:    "DeliveryFailed",
			typ:     DeliveryFailed,
			payload: DeliveryFailedPayload{GoalID: "g1", Branch: "aoa/g1", Reason: "push: rejected (fetch first)"},
			decode:  func() any { return &DeliveryFailedPayload{} },
		},
		{
			name: "SessionObserved",
			typ:  SessionObserved,
			payload: SessionObservedPayload{
				SessionID: "s-1a2b3c4d", Path: "/repo/.wt/login", Branch: "feat/login",
				Head: "abc123", Base: "def456", Files: []string{"auth.go", "auth_test.go"},
				TestFiles: []string{"auth_test.go"}, Dirty: true, Fingerprint: "f00d",
				LastChange: time.Date(2026, 9, 20, 10, 30, 0, 0, time.UTC),
			},
			decode: func() any { return &SessionObservedPayload{} },
		},
		{
			name:    "SessionObserved removed",
			typ:     SessionObserved,
			payload: SessionObservedPayload{SessionID: "s-1a2b3c4d", Path: "/repo/.wt/login", Removed: true},
			decode:  func() any { return &SessionObservedPayload{} },
		},
		{
			name:    "SessionChecked",
			typ:     SessionChecked,
			payload: SessionCheckedPayload{SessionID: "s-1a2b3c4d", Fingerprint: "f00d", AfterFingerprint: "beef", Passed: false, Command: "go test ./...", Output: "FAIL"},
			decode:  func() any { return &SessionCheckedPayload{} },
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
			// And against the payload itself: a field that does not survive
			// the wire (an untagged or "-" field) must fail here.
			if got := reflect.ValueOf(want).Elem().Interface(); !reflect.DeepEqual(got, tc.payload) {
				t.Errorf("payload lost fields on the wire:\n got  %+v\n want %+v", got, tc.payload)
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
