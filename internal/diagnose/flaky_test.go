package diagnose

import (
	"testing"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// gateRejected is how the merge queue words a real Gate verdict on the code.
const gateRejected = "verification failed: go test ./..."

// luckyRetry builds the exact history issue #104 describes: an attempt the Gate
// rejected, a retry the Gate accepted, and a merge. The caller supplies the tree
// each Gate run verified, which is what says whether the retry changed anything.
func luckyRetry(t *testing.T, failTree, failReason, passTree string) []api.Event {
	t.Helper()
	return newSeq(t).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "do it"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "g1-impl", GoalID: "g1", Title: "impl", IdempotencyKey: "g1:impl"}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "g1-impl"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g1-impl", Worker: "w"}).
		add(api.WorkStarted, api.WorkStartedPayload{TicketID: "g1-impl", Worker: "w"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g1-impl", Worker: "w", Commit: "c1"}).
		add(api.VerificationFailed, api.VerificationFailedPayload{TicketID: "g1-impl", Worker: "w", Reason: failReason, Tree: failTree}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "g1-impl"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g1-impl", Worker: "w"}).
		add(api.WorkStarted, api.WorkStartedPayload{TicketID: "g1-impl", Worker: "w"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g1-impl", Worker: "w", Commit: "c2"}).
		add(api.VerificationPassed, api.VerificationPassedPayload{TicketID: "g1-impl", Worker: "w", Tree: passTree}).
		add(api.Merged, api.MergedPayload{TicketID: "g1-impl", Worker: "w", Commit: "c2"}).
		events
}

// TestClassifyFlakyGate: the Gate rejected tree T and then accepted tree T —
// identical content, contradictory verdicts. The merge stands on a verdict the
// Gate itself contradicted, which is issue #104's "a lucky retry merged it".
func TestClassifyFlakyGate(t *testing.T) {
	f := find(t, Classify(luckyRetry(t, "T", gateRejected, "T")), FlakyGate)
	if f.Count != 1 || len(f.Tickets) != 1 || f.Tickets[0] != "g1-impl" {
		t.Fatalf("flaky gate: want 1 (g1-impl, same tree both verdicts), got %+v", f)
	}
}

// TestClassifyFlakyGateNegatives pins the guard. Everything short of the same
// content drawing both verdicts must score 0 — otherwise the mode is a rename of
// retry_churn, which already counts every rejection.
func TestClassifyFlakyGateNegatives(t *testing.T) {
	cases := []struct {
		name                           string
		failTree, failReason, passTree string
	}{
		{"the retry changed the code", "T1", gateRejected, "T2"},
		{"a log from before trees were recorded", "", gateRejected, ""},
		{"a sandbox failure is not a verdict on the code", "T", "gate could not run (sandbox failure): go test ./...", "T"},
		{"a merge conflict is not a Gate verdict", "T", "merge failed: conflict in a.go", "T"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			events := luckyRetry(t, tc.failTree, tc.failReason, tc.passTree)
			if f := find(t, Classify(events), FlakyGate); f.Count != 0 {
				t.Fatalf("%s: want 0, got %+v", tc.name, f)
			}
		})
	}
}

// TestClassifyFlakyGateTerminalFailure covers the approval path's shape: the
// Gate passed a dry run, a human approved the parked candidate, and the very
// same tree then failed. A terminal TicketFailed carries the Gate's verdict just
// as VerificationFailed does, so the contradiction must still be visible.
func TestClassifyFlakyGateTerminalFailure(t *testing.T) {
	events := newSeq(t).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "do it"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "g1-impl", GoalID: "g1", Title: "impl", IdempotencyKey: "g1:impl"}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "g1-impl"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g1-impl", Worker: "w"}).
		add(api.WorkStarted, api.WorkStartedPayload{TicketID: "g1-impl", Worker: "w"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g1-impl", Worker: "w", Commit: "c1"}).
		add(api.VerificationPassed, api.VerificationPassedPayload{TicketID: "g1-impl", Worker: "w", Tree: "T"}).
		add(api.ApprovalRequested, api.ApprovalRequestedPayload{TicketID: "g1-impl", Worker: "w", Commit: "c1"}).
		add(api.ApprovalGranted, api.ApprovalGrantedPayload{TicketID: "g1-impl", By: "human"}).
		add(api.TicketFailed, api.TicketFailedPayload{TicketID: "g1-impl", Worker: "w", Reason: "crash loop: " + gateRejected + " (×3)", Tree: "T"}).
		events
	f := find(t, Classify(events), FlakyGate)
	if f.Count != 1 || len(f.Tickets) != 1 || f.Tickets[0] != "g1-impl" {
		t.Fatalf("flaky gate: want 1 (g1-impl, dry run passed then the same tree failed), got %+v", f)
	}
}
