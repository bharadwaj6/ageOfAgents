package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	led, err := ledger.Open(filepath.Join(b.t.TempDir(), "events.jsonl"))
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
