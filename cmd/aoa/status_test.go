package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/config"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

var updateGolden = flag.Bool("update", false, "rewrite cmd/aoa/testdata golden files from the current output")

// fixtureEpoch is where every fixture log's clock starts. Fixed timestamps make
// the wall time and merge-queue waits that `aoa status` prints reproducible.
var fixtureEpoch = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// logBuilder builds an Event Log with a deterministic clock: each event lands
// the given number of seconds after the one before it, and seqs run from 1.
type logBuilder struct {
	t      *testing.T
	at     time.Time
	events []api.Event
}

func newLog(t *testing.T) *logBuilder { return &logBuilder{t: t, at: fixtureEpoch} }

// add appends an event of type typ, secs seconds after the previous event.
func (b *logBuilder) add(secs int, typ api.EventType, payload any) *logBuilder {
	b.t.Helper()
	e, err := api.NewEvent(typ, "test", payload)
	if err != nil {
		b.t.Fatalf("NewEvent %s: %v", typ, err)
	}
	b.at = b.at.Add(time.Duration(secs) * time.Second)
	e.Timestamp = b.at
	e.Seq = len(b.events) + 1
	b.events = append(b.events, e)
	return b
}

// ledger writes the events to a fresh Event Log and returns it open.
func (b *logBuilder) ledger() *ledger.Ledger {
	b.t.Helper()
	return b.writeTo(filepath.Join(b.t.TempDir(), "events.jsonl"))
}

// writeTo appends the events to the Event Log at path and returns it open.
func (b *logBuilder) writeTo(path string) *ledger.Ledger {
	b.t.Helper()
	led, err := ledger.Open(path)
	if err != nil {
		b.t.Fatalf("open ledger: %v", err)
	}
	for _, e := range b.events {
		if _, err := led.Append(e); err != nil {
			b.t.Fatalf("append %s: %v", e.Type, err)
		}
	}
	return led
}

// fixturePricing prices the two models the mixed fixture charges, in USD per
// million tokens.
var fixturePricing = map[string]float64{"m-small": 1.5, "m-large": 15}

// mixedLog is a workspace mid-run with every section `aoa status` can print:
// a merged goal; a goal decomposed into two merged children; a goal whose task
// failed with a reason and a preserved worktree; a goal awaiting approval; a
// queued goal with no task yet; token spend on two priced models; and two
// proposals in the merge queue at once. Goals are submitted in an order that
// differs from their IDs' sort order, and one child is created out of ID order,
// so a test can tell sorting by submission from sorting by ID.
func mixedLog(t *testing.T) *logBuilder {
	return newLog(t).
		add(0, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g-merged", Text: "add a greeting", Source: "human"}).
		add(1, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g-split", Text: "build the parser", Source: "linear",
			Ref: "https://linear.app/x/ENG-7", By: "octocat", IdempotencyKey: "linear:ENG-7"}).
		add(1, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g-failed", Text: "fix the flaky test", Source: "github",
			Ref: "https://github.com/o/r/issues/3"}).
		add(1, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g-await", Text: "bump the version", Source: "human"}).
		add(1, api.GoalAmended, api.GoalAmendedPayload{GoalID: "g-split", Guidance: "prefer table-driven tests"}).
		add(1, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-merged-impl", GoalID: "g-merged", Title: "Implement: add a greeting", IdempotencyKey: "g-merged:impl"}).
		add(0, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-split-impl", GoalID: "g-split", Title: "Implement: build the parser", IdempotencyKey: "g-split:impl"}).
		add(0, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-failed-impl", GoalID: "g-failed", Title: "Implement: fix the flaky test", IdempotencyKey: "g-failed:impl"}).
		add(0, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-await-impl", GoalID: "g-await", Title: "Implement: bump the version", IdempotencyKey: "g-await:impl"}).
		add(1, api.TicketReady, api.TicketReadyPayload{TicketID: "g-merged-impl"}).
		add(0, api.TicketReady, api.TicketReadyPayload{TicketID: "g-split-impl"}).
		add(0, api.TicketReady, api.TicketReadyPayload{TicketID: "g-failed-impl"}).
		add(0, api.TicketReady, api.TicketReadyPayload{TicketID: "g-await-impl"}).
		// g-merged: one attempt, merged after 3s in the queue.
		add(1, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g-merged-impl", Worker: "w1"}).
		add(0, api.WorkStarted, api.WorkStartedPayload{TicketID: "g-merged-impl", Worker: "w1", Worktree: "/ws/wt/w1"}).
		add(1, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g-split-impl", Worker: "w2"}).
		add(0, api.WorkStarted, api.WorkStartedPayload{TicketID: "g-split-impl", Worker: "w2", Worktree: "/ws/wt/w2"}).
		add(4, api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g-merged-impl", Worker: "w1", Branch: "aoa/g-merged-impl", Commit: "c-prop-1", Tokens: 1200, Model: "m-small"}).
		// g-split: decomposed into two children, created out of ID order.
		add(1, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-split-impl/b", GoalID: "g-split", Title: "write the lexer", IdempotencyKey: "g-split:b", CreatedBy: "w2", Depth: 1}).
		add(0, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-split-impl/a", GoalID: "g-split", Title: "write the grammar", IdempotencyKey: "g-split:a", CreatedBy: "w2", Depth: 1}).
		add(0, api.TicketDecomposed, api.TicketDecomposedPayload{TicketID: "g-split-impl", Worker: "w2", Children: []string{"g-split-impl/b", "g-split-impl/a"}, Tokens: 300, Model: "m-large"}).
		add(1, api.VerificationPassed, api.VerificationPassedPayload{TicketID: "g-merged-impl", Worker: "w1"}).
		add(1, api.Merged, api.MergedPayload{TicketID: "g-merged-impl", Worker: "w1", Commit: "c0ffee1"}).
		// g-failed: a rejected proposal, a retry, then a terminal failure that
		// keeps its worktree for a human.
		add(1, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g-failed-impl", Worker: "w3"}).
		add(0, api.WorkStarted, api.WorkStartedPayload{TicketID: "g-failed-impl", Worker: "w3", Worktree: "/ws/wt/w3"}).
		add(2, api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g-failed-impl", Worker: "w3", Branch: "aoa/g-failed-impl", Commit: "c-prop-2", Tokens: 500, Model: "m-small"}).
		add(2, api.VerificationFailed, api.VerificationFailedPayload{TicketID: "g-failed-impl", Worker: "w3", Reason: "gate: go test ./... failed"}).
		add(1, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g-failed-impl", Worker: "w4"}).
		add(0, api.WorkStarted, api.WorkStartedPayload{TicketID: "g-failed-impl", Worker: "w4", Worktree: "/ws/wt/w4"}).
		add(3, api.TicketFailed, api.TicketFailedPayload{TicketID: "g-failed-impl", Worker: "w4", Reason: "gate: go test ./... failed (2 attempts)",
			Worktree: "/ws/.aoa/handoff/g-failed-impl", Tokens: 700, Model: "m-small"}).
		// g-split's children: both proposed before either resolves (queue depth 2).
		add(1, api.TicketReady, api.TicketReadyPayload{TicketID: "g-split-impl/b"}).
		add(0, api.TicketReady, api.TicketReadyPayload{TicketID: "g-split-impl/a"}).
		add(0, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g-split-impl/a", Worker: "w5"}).
		add(0, api.WorkStarted, api.WorkStartedPayload{TicketID: "g-split-impl/a", Worker: "w5", Worktree: "/ws/wt/w5"}).
		add(0, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g-split-impl/b", Worker: "w6"}).
		add(0, api.WorkStarted, api.WorkStartedPayload{TicketID: "g-split-impl/b", Worker: "w6", Worktree: "/ws/wt/w6"}).
		add(5, api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g-split-impl/a", Worker: "w5", Branch: "aoa/a", Commit: "c-prop-3", Tokens: 400, Model: "m-large"}).
		add(1, api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g-split-impl/b", Worker: "w6", Branch: "aoa/b", Commit: "c-prop-4", Tokens: 600, Model: "m-small"}).
		add(1, api.VerificationPassed, api.VerificationPassedPayload{TicketID: "g-split-impl/a", Worker: "w5"}).
		add(1, api.Merged, api.MergedPayload{TicketID: "g-split-impl/a", Worker: "w5", Commit: "a11ce01"}).
		add(1, api.VerificationPassed, api.VerificationPassedPayload{TicketID: "g-split-impl/b", Worker: "w6"}).
		add(1, api.Merged, api.MergedPayload{TicketID: "g-split-impl/b", Worker: "w6", Commit: "b0b0b02"}).
		// g-await: verified, then parked for a human.
		add(1, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g-await-impl", Worker: "w7"}).
		add(0, api.WorkStarted, api.WorkStartedPayload{TicketID: "g-await-impl", Worker: "w7", Worktree: "/ws/wt/w7"}).
		add(2, api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g-await-impl", Worker: "w7", Branch: "aoa/g-await-impl", Commit: "c-prop-5", Tokens: 250, Model: "m-small"}).
		add(1, api.VerificationPassed, api.VerificationPassedPayload{TicketID: "g-await-impl", Worker: "w7"}).
		add(0, api.ApprovalRequested, api.ApprovalRequestedPayload{TicketID: "g-await-impl", Worker: "w7", Commit: "c-prop-5"}).
		// g-queued: submitted last, no task yet.
		add(2, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g-queued", Text: "write the docs", Source: "human"})
}

// settledLog is a finished run whose one task failed without a reason or a
// worktree and never reached the merge queue, on an unpriced backend: the
// "all work settled" line prints, and the cost and merge-queue lines do not.
func settledLog(t *testing.T) *logBuilder {
	return newLog(t).
		add(0, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g-1", Text: "do the thing", Source: "human"}).
		add(1, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-1-impl", GoalID: "g-1", Title: "Implement: do the thing", IdempotencyKey: "g-1:impl"}).
		add(1, api.TicketReady, api.TicketReadyPayload{TicketID: "g-1-impl"}).
		add(1, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g-1-impl", Worker: "w1"}).
		add(0, api.WorkStarted, api.WorkStartedPayload{TicketID: "g-1-impl", Worker: "w1", Worktree: "/ws/wt/w1"}).
		add(7, api.TicketFailed, api.TicketFailedPayload{TicketID: "g-1-impl", Worker: "w1", Tokens: 90})
}

// TestStatusTextGolden pins what `aoa status` prints, byte for byte, for
// fixture logs that exercise every line it can print. It exists to prove a
// refactor of the status code leaves the human output unchanged; if the text
// changes on purpose, rewrite the golden files with -update and review the diff.
func TestStatusTextGolden(t *testing.T) {
	tests := []struct {
		name        string
		log         func(*testing.T) *logBuilder
		pricing     map[string]float64
		golden      string
		wantSettled bool
		wantFailed  int
	}{
		{name: "empty workspace", log: newLog, golden: "status_empty.txt", wantSettled: true},
		{name: "mid-run, every section", log: mixedLog, pricing: fixturePricing, golden: "status_mixed.txt", wantFailed: 1},
		{name: "settled, unpriced", log: settledLog, golden: "status_settled.txt", wantSettled: true, wantFailed: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			led := tt.log(t).ledger()
			var (
				settled bool
				failed  int
				err     error
			)
			out := captureStdout(t, func() { settled, failed, err = printStatus(led, tt.pricing) })
			if err != nil {
				t.Fatalf("printStatus: %v", err)
			}
			if settled != tt.wantSettled || failed != tt.wantFailed {
				t.Errorf("printStatus = (settled %v, failed %d), want (%v, %d)", settled, failed, tt.wantSettled, tt.wantFailed)
			}
			path := filepath.Join("testdata", tt.golden)
			if *updateGolden {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatalf("mkdir testdata: %v", err)
				}
				if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (run with -update to create it): %v", err)
			}
			if out != string(want) {
				t.Errorf("aoa status output changed:\n--- got ---\n%s--- want (%s) ---\n%s", out, path, want)
			}
		})
	}
}

// partialLog is a goal decomposed into two children, one merged and one failed:
// partial success, which is a failed outcome that still lists the merged commit.
func partialLog(t *testing.T) *logBuilder {
	return newLog(t).
		add(0, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g-1", Text: "split me", Source: "human"}).
		add(1, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-1-impl", GoalID: "g-1", Title: "Implement: split me", IdempotencyKey: "g-1:impl"}).
		add(1, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g-1-impl", Worker: "w1"}).
		add(1, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-1-impl/a", GoalID: "g-1", Title: "half a", IdempotencyKey: "g-1:a", Depth: 1}).
		add(0, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-1-impl/b", GoalID: "g-1", Title: "half b", IdempotencyKey: "g-1:b", Depth: 1}).
		add(0, api.TicketDecomposed, api.TicketDecomposedPayload{TicketID: "g-1-impl", Worker: "w1", Children: []string{"g-1-impl/a", "g-1-impl/b"}}).
		add(1, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g-1-impl/a", Worker: "w2"}).
		add(1, api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g-1-impl/a", Worker: "w2", Commit: "cand-a"}).
		add(1, api.Merged, api.MergedPayload{TicketID: "g-1-impl/a", Worker: "w2", Commit: "aaa1111"}).
		add(1, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g-1-impl/b", Worker: "w3"}).
		add(1, api.TicketFailed, api.TicketFailedPayload{TicketID: "g-1-impl/b", Worker: "w3", Reason: "gate: build failed", Worktree: "/ws/handoff/b"})
}

// rejectedLog is a goal whose only task a human rejected at the approval gate.
func rejectedLog(t *testing.T) *logBuilder {
	return newLog(t).
		add(0, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g-1", Text: "risky change", Source: "slack", By: "alice"}).
		add(1, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-1-impl", GoalID: "g-1", Title: "Implement: risky change", IdempotencyKey: "g-1:impl"}).
		add(1, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g-1-impl", Worker: "w1"}).
		add(1, api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g-1-impl", Worker: "w1", Commit: "cand-1"}).
		add(1, api.ApprovalRequested, api.ApprovalRequestedPayload{TicketID: "g-1-impl", Worker: "w1", Commit: "cand-1"}).
		add(5, api.ApprovalDenied, api.ApprovalDeniedPayload{TicketID: "g-1-impl", By: "bob", Reason: "not now"})
}

// queuedLog is a goal just submitted: the Scheduler has not created its task.
func queuedLog(t *testing.T) *logBuilder {
	return newLog(t).
		add(0, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g-1", Text: "later", Source: "human"})
}

// cancelledLog is a goal cancelled while its only task's attempt ran; with
// failed, the Scheduler has since failed that task.
func cancelledLog(failed bool) func(t *testing.T) *logBuilder {
	return func(t *testing.T) *logBuilder {
		b := newLog(t).
			add(0, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g-1", Text: "withdrawn", Source: "linear"}).
			add(1, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-1-impl", GoalID: "g-1", Title: "Implement: withdrawn", IdempotencyKey: "g-1:impl"}).
			add(1, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g-1-impl", Worker: "w1"}).
			add(1, api.GoalCancelled, api.GoalCancelledPayload{GoalID: "g-1", Reason: "issue closed"})
		if failed {
			b.add(1, api.TicketFailed, api.TicketFailedPayload{TicketID: "g-1-impl", Worker: "w1", Reason: "goal cancelled"})
		}
		return b
	}
}

// deliveryLog is a goal whose only task merged onto its Goal branch in
// pull-request delivery mode, and whose first delivery failed; with delivered,
// a later run has delivered it.
func deliveryLog(delivered bool) func(t *testing.T) *logBuilder {
	return func(t *testing.T) *logBuilder {
		b := newLog(t).
			add(0, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g-1", Text: "ship it", Source: "github"}).
			add(1, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-1-impl", GoalID: "g-1", Title: "Implement: ship it", IdempotencyKey: "g-1:impl"}).
			add(1, api.TicketClaimed, api.TicketClaimedPayload{TicketID: "g-1-impl", Worker: "w1"}).
			add(1, api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g-1-impl", Worker: "w1", Commit: "cand-1"}).
			add(1, api.Merged, api.MergedPayload{TicketID: "g-1-impl", Worker: "w1", Commit: "m1", Branch: "aoa/g-1"}).
			add(1, api.DeliveryFailed, api.DeliveryFailedPayload{GoalID: "g-1", Branch: "aoa/g-1", Reason: "push aoa/g-1: exit status 1\n ! [rejected] aoa/g-1 (fetch first)"})
		if delivered {
			b.add(1, api.Delivered, api.DeliveredPayload{GoalID: "g-1", Branch: "aoa/g-1", Commit: "m1", URL: "https://github.com/o/r/pull/9"})
		}
		return b
	}
}

// goalWant is what TestStatusViewProjection expects of one GoalView; Tickets
// lists its task ids in order.
type goalWant struct {
	ID, Outcome, Source, Ref, By string
	Tokens                       int
	CostUSD                      float64
	Amendments, Commits, Tickets []string
	Branch, PRURL, DeliveryError string
	Graph                        api.GraphView
}

func TestStatusViewProjection(t *testing.T) {
	tests := []struct {
		name        string
		log         func(*testing.T) *logBuilder
		pricing     map[string]float64
		wantSettled bool
		wantGoals   []goalWant
		wantTickets map[string]api.TicketView // spot checks, compared whole
		wantTotals  api.StatusTotals
		wantQueue   api.MergeQueueView
	}{
		{
			name: "empty workspace", log: newLog, wantSettled: true,
		},
		{
			name: "mid-run, every section", log: mixedLog, pricing: fixturePricing,
			wantGoals: []goalWant{ // submission order, not id order
				{ID: "g-merged", Outcome: api.OutcomeMerged, Source: "human", Tokens: 1200, CostUSD: 1200 * 1.5 / 1e6,
					Commits: []string{"c0ffee1"}, Tickets: []string{"g-merged-impl"}},
				{ID: "g-split", Outcome: api.OutcomeMerged, Source: "linear", Ref: "https://linear.app/x/ENG-7", By: "octocat",
					Tokens: 1300, CostUSD: 700*15/1e6 + 600*1.5/1e6, Amendments: []string{"prefer table-driven tests"},
					Commits: []string{"b0b0b02", "a11ce01"}, Tickets: []string{"g-split-impl", "g-split-impl/b", "g-split-impl/a"},
					Graph: api.GraphView{MaxDepth: 1, MaxFanOut: 2}},
				{ID: "g-failed", Outcome: api.OutcomeFailed, Source: "github", Ref: "https://github.com/o/r/issues/3",
					Tokens: 1200, CostUSD: 1200 * 1.5 / 1e6, Tickets: []string{"g-failed-impl"}},
				{ID: "g-await", Outcome: api.OutcomeAwaitingApproval, Source: "human", Tokens: 250, CostUSD: 250 * 1.5 / 1e6,
					Tickets: []string{"g-await-impl"}},
				{ID: "g-queued", Outcome: api.OutcomeQueued, Source: "human"},
			},
			wantTickets: map[string]api.TicketView{
				"g-failed-impl": {ID: "g-failed-impl", Title: "Implement: fix the flaky test", Status: "failed", Attempts: 2, Tokens: 1200,
					FailReason: "gate: go test ./... failed (2 attempts)", Worktree: "/ws/.aoa/handoff/g-failed-impl"},
				"g-await-impl": {ID: "g-await-impl", Title: "Implement: bump the version", Status: "awaiting", Attempts: 1, Tokens: 250,
					Commit: "c-prop-5"},
				"g-split-impl": {ID: "g-split-impl", Title: "Implement: build the parser", Status: "decomposed", Attempts: 1, Tokens: 300},
				"g-split-impl/a": {ID: "g-split-impl/a", Title: "write the grammar", Status: "merged", Attempts: 1, Tokens: 400, Depth: 1,
					Commit: "a11ce01"},
			},
			wantTotals: api.StatusTotals{Tokens: 3950, CostUSD: 3250*1.5/1e6 + 700*15/1e6, WallSeconds: 41,
				Goals: 5, Tickets: 6, Merged: 3, Failed: 1, Awaiting: 1},
			wantQueue: api.MergeQueueView{MaxDepth: 2, WaitMeanSeconds: 2.6, WaitMaxSeconds: 4},
		},
		{
			name: "decomposed, partial success", log: partialLog, wantSettled: true,
			wantGoals: []goalWant{{ID: "g-1", Outcome: api.OutcomeFailed, Source: "human", Commits: []string{"aaa1111"},
				Tickets: []string{"g-1-impl", "g-1-impl/a", "g-1-impl/b"}, Graph: api.GraphView{MaxDepth: 1, MaxFanOut: 2}}},
			wantTickets: map[string]api.TicketView{
				"g-1-impl/b": {ID: "g-1-impl/b", Title: "half b", Status: "failed", Attempts: 1, Depth: 1,
					FailReason: "gate: build failed", Worktree: "/ws/handoff/b"},
			},
			wantTotals: api.StatusTotals{WallSeconds: 8, Goals: 1, Tickets: 3, Merged: 1, Failed: 1},
			wantQueue:  api.MergeQueueView{MaxDepth: 1, WaitMeanSeconds: 1, WaitMaxSeconds: 1},
		},
		{
			name: "only task rejected by a human", log: rejectedLog, wantSettled: true,
			wantGoals: []goalWant{{ID: "g-1", Outcome: api.OutcomeFailed, Source: "slack", By: "alice", Tickets: []string{"g-1-impl"}}},
			wantTickets: map[string]api.TicketView{
				"g-1-impl": {ID: "g-1-impl", Title: "Implement: risky change", Status: "failed", Attempts: 1, Rejected: true},
			},
			wantTotals: api.StatusTotals{WallSeconds: 9, Goals: 1, Tickets: 1, Failed: 1},
			wantQueue:  api.MergeQueueView{MaxDepth: 1, WaitMeanSeconds: 1, WaitMaxSeconds: 1},
		},
		{
			name:       "cancelled, attempt still in flight",
			log:        cancelledLog(false),
			wantGoals:  []goalWant{{ID: "g-1", Outcome: api.OutcomeCancelled, Source: "linear", Tickets: []string{"g-1-impl"}}},
			wantTotals: api.StatusTotals{WallSeconds: 3, Goals: 1, Tickets: 1},
		},
		{
			name: "cancelled, nothing in flight", log: cancelledLog(true), wantSettled: true,
			wantGoals: []goalWant{{ID: "g-1", Outcome: api.OutcomeCancelled, Source: "linear", Tickets: []string{"g-1-impl"}}},
			wantTickets: map[string]api.TicketView{
				"g-1-impl": {ID: "g-1-impl", Title: "Implement: withdrawn", Status: "failed", Attempts: 1, FailReason: "goal cancelled"},
			},
			wantTotals: api.StatusTotals{WallSeconds: 4, Goals: 1, Tickets: 1, Failed: 1},
		},
		{
			name:       "queued goal is not settled",
			log:        queuedLog,
			wantGoals:  []goalWant{{ID: "g-1", Outcome: api.OutcomeQueued, Source: "human"}},
			wantTotals: api.StatusTotals{Goals: 1},
		},
		{
			name: "delivery failed, still pending", log: deliveryLog(false),
			wantGoals: []goalWant{{ID: "g-1", Outcome: api.OutcomeRunning, Source: "github", Commits: []string{"m1"},
				Tickets: []string{"g-1-impl"}, Branch: "aoa/g-1", DeliveryError: "push aoa/g-1: exit status 1\n ! [rejected] aoa/g-1 (fetch first)"}},
			wantTotals: api.StatusTotals{WallSeconds: 5, Goals: 1, Tickets: 1, Merged: 1},
			wantQueue:  api.MergeQueueView{MaxDepth: 1, WaitMeanSeconds: 1, WaitMaxSeconds: 1},
		},
		{
			name: "delivered", log: deliveryLog(true), wantSettled: true,
			wantGoals: []goalWant{{ID: "g-1", Outcome: api.OutcomeDelivered, Source: "github", Commits: []string{"m1"},
				Tickets: []string{"g-1-impl"}, Branch: "aoa/g-1", PRURL: "https://github.com/o/r/pull/9"}},
			wantTotals: api.StatusTotals{WallSeconds: 6, Goals: 1, Tickets: 1, Merged: 1},
			wantQueue:  api.MergeQueueView{MaxDepth: 1, WaitMeanSeconds: 1, WaitMaxSeconds: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := tt.log(t).events
			v, err := statusView(events, tt.pricing)
			if err != nil {
				t.Fatalf("statusView: %v", err)
			}
			if v.Schema != api.ContractVersion {
				t.Errorf("schema = %d, want %d", v.Schema, api.ContractVersion)
			}
			if v.LastSeq != len(events) {
				t.Errorf("last_seq = %d, want %d", v.LastSeq, len(events))
			}
			submittedAt := map[string]time.Time{}
			for _, e := range events {
				var p api.GoalSubmittedPayload
				if e.Type == api.GoalSubmitted && e.DecodePayload(&p) == nil {
					submittedAt[p.GoalID] = e.Timestamp
				}
			}
			if v.Settled != tt.wantSettled {
				t.Errorf("settled = %v, want %v", v.Settled, tt.wantSettled)
			}
			if v.Goals == nil {
				t.Error("goals is nil; an empty workspace must encode as [], not null")
			}
			if len(v.Goals) != len(tt.wantGoals) {
				t.Fatalf("got %d goals, want %d: %+v", len(v.Goals), len(tt.wantGoals), v.Goals)
			}
			var goalTokens int
			var goalCost float64
			for i, want := range tt.wantGoals {
				g := v.Goals[i]
				got := goalWant{ID: g.ID, Outcome: g.Outcome, Source: g.Source, Ref: g.Ref, By: g.By, Tokens: g.Tokens,
					Amendments: g.Amendments, Commits: g.Commits, Graph: g.Graph,
					Branch: g.Branch, PRURL: g.PRURL, DeliveryError: g.DeliveryError}
				for _, tv := range g.Tickets {
					got.Tickets = append(got.Tickets, tv.ID)
				}
				if g.Tickets == nil {
					t.Errorf("goal %s: tickets is nil, want [] when empty", g.ID)
				}
				if !floatNear(g.CostUSD, want.CostUSD) {
					t.Errorf("goal %s: cost_usd = %v, want %v", g.ID, g.CostUSD, want.CostUSD)
				}
				want.CostUSD = 0 // compared above, within float tolerance
				if !reflect.DeepEqual(got, want) {
					t.Errorf("goal %d:\n got  %+v\n want %+v", i, got, want)
				}
				if want := submittedAt[g.ID]; !g.SubmittedAt.Equal(want) {
					t.Errorf("goal %s: submitted_at = %v, want %v", g.ID, g.SubmittedAt, want)
				}
				goalTokens += g.Tokens
				goalCost += g.CostUSD
				for _, tv := range g.Tickets {
					if want, ok := tt.wantTickets[tv.ID]; ok && tv != want {
						t.Errorf("ticket %s:\n got  %+v\n want %+v", tv.ID, tv, want)
					}
				}
			}
			// One accounting: the goals add up to the totals.
			if goalTokens != v.Totals.Tokens || !floatNear(goalCost, v.Totals.CostUSD) {
				t.Errorf("goals sum to %d tokens / $%v, totals say %d / $%v", goalTokens, goalCost, v.Totals.Tokens, v.Totals.CostUSD)
			}
			if !floatNear(v.Totals.CostUSD, tt.wantTotals.CostUSD) {
				t.Errorf("totals.cost_usd = %v, want %v", v.Totals.CostUSD, tt.wantTotals.CostUSD)
			}
			gotTotals, wantTotals := v.Totals, tt.wantTotals
			gotTotals.CostUSD, wantTotals.CostUSD = 0, 0
			if gotTotals != wantTotals {
				t.Errorf("totals:\n got  %+v\n want %+v", gotTotals, wantTotals)
			}
			if !floatNear(v.MergeQueue.WaitMeanSeconds, tt.wantQueue.WaitMeanSeconds) ||
				v.MergeQueue.MaxDepth != tt.wantQueue.MaxDepth || v.MergeQueue.WaitMaxSeconds != tt.wantQueue.WaitMaxSeconds {
				t.Errorf("merge_queue = %+v, want %+v", v.MergeQueue, tt.wantQueue)
			}
		})
	}
}

// floatNear reports whether two dollar or second amounts agree to well below
// anything status prints.
func floatNear(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// The text output keeps its own, older notion of settled — no task in flight —
// so a just-submitted goal still prints "all work settled" and ends --watch,
// while the JSON view reports it unsettled until it reaches an outcome.
func TestQueuedGoalTextStillSaysSettled(t *testing.T) {
	var settled bool
	var err error
	out := captureStdout(t, func() { settled, _, err = printStatus(queuedLog(t).ledger(), nil) })
	if err != nil {
		t.Fatalf("printStatus: %v", err)
	}
	if !settled || !strings.HasSuffix(out, "all work settled\n") {
		t.Errorf("printStatus settled = %v, output:\n%s", settled, out)
	}
}

// statusWorkspace is a workspace whose Event Log holds b's events and whose
// config prices the fixture models.
func statusWorkspace(t *testing.T, b *logBuilder) (root string, events []api.Event) {
	t.Helper()
	root, ledgerPath := bareWorkspace(t)
	cfg := "repo = \"./repo\"\n\n[pricing]\nm-small = 1.5\nm-large = 15\n"
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	events, err := b.writeTo(ledgerPath).Read()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return root, events
}

// A program reads `aoa status --json` with one decode: exactly one line, one
// JSON value, with nothing else on stdout, carrying the same view statusView
// builds — priced from the workspace config.
func TestStatusJSONStdoutIsOneJSONValue(t *testing.T) {
	tests := []struct {
		name string
		log  func(*testing.T) *logBuilder
	}{
		{name: "empty workspace", log: newLog},
		{name: "mid-run", log: mixedLog},
		{name: "partial success", log: partialLog},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, events := statusWorkspace(t, tt.log(t))
			var err error
			out := captureStdout(t, func() { err = cmdStatus([]string{"--path", root, "--json"}) })
			if err != nil {
				t.Fatalf("aoa status --json: %v", err)
			}
			var got api.StatusView
			decodeJSONLine(t, out, &got)
			want, err := statusView(events, fixturePricing)
			if err != nil {
				t.Fatalf("statusView: %v", err)
			}
			wantJSON, err := json.Marshal(want)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Errorf("stdout view differs from statusView:\n got  %s\n want %s", gotJSON, wantJSON)
			}
			if !strings.Contains(out, `"goals":[`) {
				t.Errorf("goals must be a JSON array, never null: %s", out)
			}
		})
	}
}

// --json is one snapshot. Streaming belongs to `aoa events --follow`, so asking
// for both is refused up front, whichever order the flags come in, and nothing
// is printed. The log is settled, so were the pair accepted, --watch would
// render once and return rather than hang the test.
func TestStatusJSONRejectsWatch(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "json then watch", args: []string{"--json", "--watch"}},
		{name: "watch then json", args: []string{"--watch", "--json"}},
		{name: "with an interval", args: []string{"--json", "--watch", "--interval", "1s"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := statusWorkspace(t, settledLog(t))
			var err error
			out := captureStdout(t, func() { err = cmdStatus(append([]string{"--path", root}, tt.args...)) })
			if err == nil {
				t.Fatalf("aoa status %v succeeded; want a usage error", tt.args)
			}
			if !strings.Contains(err.Error(), "--watch") || !strings.Contains(err.Error(), "events") {
				t.Errorf("error %q should name --watch and point at aoa events", err)
			}
			if out != "" {
				t.Errorf("printed %q before refusing", out)
			}
		})
	}
}

// `aoa run` exits 1 when printStatus reports failed tasks, so a cron job can
// alert on it. A cancel is a front door's deliberate choice, not a failure: its
// tasks must not count. A human rejection still does, and so does a delivery
// that is stuck, until a later run delivers it.
func TestPrintStatusFailedExcludesCancelledGoals(t *testing.T) {
	tests := []struct {
		name string
		log  func(*testing.T) *logBuilder
		want int
	}{
		{"cancelled goal", cancelledLog(true), 0},
		{"rejected goal", rejectedLog, 1},
		{"delivery failed", deliveryLog(false), 1},
		{"delivered after a failed delivery", deliveryLog(true), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			led := tt.log(t).ledger()
			var failed int
			captureStdout(t, func() {
				var err error
				if _, failed, err = printStatus(led, nil); err != nil {
					t.Fatalf("printStatus: %v", err)
				}
			})
			if failed != tt.want {
				t.Errorf("failed = %d, want %d", failed, tt.want)
			}
		})
	}
}

// The text output names the pull request once a goal is delivered, and why
// delivery is still pending after a failure — indented under the goal, though
// git's output runs to several lines. Both appear only in pull-request
// delivery mode, so the local-mode goldens above never change.
func TestStatusTextShowsDelivery(t *testing.T) {
	tests := []struct {
		name, want, notWant string
		log                 func(*testing.T) *logBuilder
	}{
		{name: "pending", log: deliveryLog(false), want: "goal g-1: ship it\n  delivery pending: push aoa/g-1: exit status 1\n     ! [rejected] aoa/g-1 (fetch first)\n", notWant: "  pr: "},
		{name: "delivered", log: deliveryLog(true), want: "goal g-1: ship it\n  pr: https://github.com/o/r/pull/9\n", notWant: "delivery pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := statusView(tt.log(t).events, nil)
			if err != nil {
				t.Fatalf("statusView: %v", err)
			}
			var b bytes.Buffer
			if err := renderStatus(&b, v); err != nil {
				t.Fatalf("renderStatus: %v", err)
			}
			if out := b.String(); !strings.Contains(out, tt.want) || strings.Contains(out, tt.notWant) {
				t.Errorf("status text should contain %q and not %q:\n%s", tt.want, tt.notWant, out)
			}
		})
	}
}
