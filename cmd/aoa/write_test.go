package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/config"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
	"github.com/stretchr/testify/require"
)

// countLogEvents reads the log at path and counts the events of type typ.
func countLogEvents(t *testing.T, path string, typ api.EventType) int {
	t.Helper()
	led, err := ledger.Open(path)
	require.NoError(t, err)
	events, err := led.Read()
	require.NoError(t, err)
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// A board poller re-submits the same issue every cycle, and a second poller (or
// a retry) can overlap with it. However many callers race on one key, exactly
// one GoalSubmitted may land, and every caller must learn the same Goal id.
// Each goroutine has its own Ledger handle, as separate `aoa goal` processes do.
func TestConcurrentKeyedSubmitsYieldOneGoal(t *testing.T) {
	const callers = 8
	path := filepath.Join(t.TempDir(), "events.jsonl")

	handles := make([]*ledger.Ledger, callers)
	for i := range handles {
		led, err := ledger.Open(path)
		require.NoError(t, err)
		handles[i] = led
	}
	results := make([]api.SubmitResult, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, led := range handles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i], errs[i] = submitGoal(led, goalRequest{
				Text: "fix the flaky test", Source: "linear", Key: "linear:ENG-1",
			})
		}()
	}
	close(start)
	wg.Wait()

	fresh := 0
	for i, res := range results {
		require.NoError(t, errs[i], "caller %d", i)
		require.Equal(t, results[0].GoalID, res.GoalID, "caller %d got a different goal", i)
		require.Equal(t, api.ContractVersion, res.Schema)
		if !res.Duplicate {
			fresh++
		}
	}
	require.Equal(t, 1, fresh, "exactly one caller should have created the goal: %+v", results)
	require.Equal(t, 1, countLogEvents(t, path, api.GoalSubmitted), "GoalSubmitted events on the log")
	for i, res := range results {
		require.Equal(t, 1, res.Seq, "caller %d: seq should name the one GoalSubmitted", i)
	}
}

// bareWorkspace is a workspace with a config and nothing else: enough for the
// write verbs, which only touch the Event Log, and needs no git.
func bareWorkspace(t *testing.T) (root, ledgerPath string) {
	t.Helper()
	root = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, config.FileName), []byte("repo = \"./repo\"\n"), 0o644))
	ws, err := workspaceAt(root)
	require.NoError(t, err)
	return root, ws.ledgerPath
}

// decodeJSONLine asserts out is exactly one JSON line and decodes it into v.
func decodeJSONLine(t *testing.T, out string, v any) {
	t.Helper()
	require.True(t, strings.HasSuffix(out, "\n"), "output should end in a newline: %q", out)
	require.Equal(t, 1, strings.Count(out, "\n"), "output should be one line: %q", out)
	dec := json.NewDecoder(strings.NewReader(out))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(v), "output: %q", out)
}

func TestGoalJSONReportsDuplicate(t *testing.T) {
	root, ledgerPath := bareWorkspace(t)
	// Steps run in order against one workspace.
	steps := []struct {
		name      string
		key       string
		sameAs    int // index of the earlier step whose goal this must be; -1 for a new goal
		wantDup   bool
		wantSeq   int
		wantGoals int // GoalSubmitted events on the log afterwards
	}{
		{name: "new key", key: "linear:ENG-1", sameAs: -1, wantSeq: 1, wantGoals: 1},
		{name: "same key again", key: "linear:ENG-1", sameAs: 0, wantDup: true, wantSeq: 1, wantGoals: 1},
		{name: "different key", key: "linear:ENG-2", sameAs: -1, wantSeq: 2, wantGoals: 2},
	}
	got := make([]api.SubmitResult, len(steps))
	for i, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() {
				err = cmdGoal([]string{"--path", root, "--json", "--source", "linear", "--key", st.key,
					"--ref", "https://linear.app/acme/issue/" + st.key, "--by", "octocat", "fix", "the", "flaky", "test"})
			})
			require.NoError(t, err)
			decodeJSONLine(t, out, &got[i])
			res := got[i]
			require.Equal(t, api.ContractVersion, res.Schema)
			require.Equal(t, st.wantDup, res.Duplicate)
			require.Equal(t, st.wantSeq, res.Seq)
			if st.sameAs >= 0 {
				require.Equal(t, got[st.sameAs].GoalID, res.GoalID)
			}
			for j := 0; j < i; j++ {
				if st.sameAs != j {
					require.NotEqual(t, got[j].GoalID, res.GoalID, "step %d reused goal of step %d", i, j)
				}
			}
			require.Equal(t, st.wantGoals, countLogEvents(t, ledgerPath, api.GoalSubmitted))
		})
	}

	// Without --json a duplicate says so, rather than claiming a new submission.
	var err error
	out := captureStdout(t, func() {
		err = cmdGoal([]string{"--path", root, "--key", "linear:ENG-1", "fix it again"})
	})
	require.NoError(t, err)
	require.Equal(t, fmt.Sprintf("goal %s already submitted (key %q)\n", got[0].GoalID, "linear:ENG-1"), out)

	// The origin flags land on the Goal a replay builds.
	led, err := ledger.Open(ledgerPath)
	require.NoError(t, err)
	events, err := led.Read()
	require.NoError(t, err)
	s, err := state.Fold(events)
	require.NoError(t, err)
	g := s.Goals[got[0].GoalID]
	require.NotNil(t, g)
	require.Equal(t, "linear", g.Source)
	require.Equal(t, "https://linear.app/acme/issue/linear:ENG-1", g.Ref)
	require.Equal(t, "octocat", g.By)
}

func TestAmendJSON(t *testing.T) {
	root, ledgerPath := bareWorkspace(t)
	led, err := ledger.Open(ledgerPath)
	require.NoError(t, err)
	submitted, err := submitGoal(led, goalRequest{Text: "build a parser", Source: "human"})
	require.NoError(t, err)

	tests := []struct {
		name    string
		goalID  string
		wantErr string
		wantSeq int
	}{
		{name: "known goal", goalID: submitted.GoalID, wantSeq: 2},
		{name: "unknown goal", goalID: "g-nope", wantErr: `unknown goal "g-nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := countLogEvents(t, ledgerPath, api.GoalAmended)
			var err error
			out := captureStdout(t, func() {
				err = cmdAmend([]string{"--path", root, "--json", tt.goalID, "handle", "comments"})
			})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Empty(t, out)
				require.Equal(t, before, countLogEvents(t, ledgerPath, api.GoalAmended), "a failed amend appended")
				return
			}
			require.NoError(t, err)
			var res api.AmendResult
			decodeJSONLine(t, out, &res)
			require.Equal(t, api.AmendResult{Schema: api.ContractVersion, GoalID: tt.goalID, Seq: tt.wantSeq}, res)
			require.Equal(t, before+1, countLogEvents(t, ledgerPath, api.GoalAmended))
		})
	}
}

// parkTicket puts a ticket awaiting approval on the log and returns its id.
func parkTicket(t *testing.T, ledgerPath string) string {
	t.Helper()
	led, err := ledger.Open(ledgerPath)
	require.NoError(t, err)
	const ticket = "g1-impl"
	for _, ev := range []struct {
		typ     api.EventType
		payload any
	}{
		{api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "add a greeting"}},
		{api.TicketCreated, api.TicketCreatedPayload{TicketID: ticket, GoalID: "g1", Title: "impl"}},
		{api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: ticket, Worker: "w1", Branch: "aoa/" + ticket}},
		{api.ApprovalRequested, api.ApprovalRequestedPayload{TicketID: ticket, Worker: "w1"}},
	} {
		e, err := api.NewEvent(ev.typ, "test", ev.payload)
		require.NoError(t, err)
		_, err = led.Append(e)
		require.NoError(t, err)
	}
	return ticket
}

// decide runs `aoa approve` or `aoa reject` with --json plus extra flags and
// returns the decoded result.
func decide(t *testing.T, root, ticket string, approve bool, flags ...string) (api.DecisionResult, error) {
	t.Helper()
	args := append(append([]string{"--path", root, "--json"}, flags...), ticket)
	var err error
	out := captureStdout(t, func() { err = cmdApprove(args, approve) })
	var res api.DecisionResult
	if err == nil {
		decodeJSONLine(t, out, &res)
	}
	return res, err
}

// lastPayload decodes the payload of the last event of type typ on the log.
func lastPayload(t *testing.T, ledgerPath string, typ api.EventType, v any) {
	t.Helper()
	led, err := ledger.Open(ledgerPath)
	require.NoError(t, err)
	events, err := led.Read()
	require.NoError(t, err)
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == typ {
			require.NoError(t, events[i].DecodePayload(v))
			return
		}
	}
	t.Fatalf("no %s event on the log", typ)
}

func TestApproveRecordsByAndReason(t *testing.T) {
	tests := []struct {
		name       string
		approve    bool
		flags      []string
		wantBy     string
		wantReason string
	}{
		{name: "approve", approve: true, flags: []string{"--by", "octocat", "--reason", "reviewed the diff"},
			wantBy: "octocat", wantReason: "reviewed the diff"},
		{name: "approve without by or reason", approve: true},
		{name: "reject", flags: []string{"--by", "octocat", "--reason", "wrong approach"},
			wantBy: "octocat", wantReason: "wrong approach"},
		{name: "reject without a reason", flags: []string{"--by", "octocat"},
			wantBy: "octocat", wantReason: "rejected by operator"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, ledgerPath := bareWorkspace(t)
			ticket := parkTicket(t, ledgerPath)

			res, err := decide(t, root, ticket, tt.approve, tt.flags...)
			require.NoError(t, err)
			want := api.DecisionResult{Schema: api.ContractVersion, TicketID: ticket, Decision: api.DecisionRejected, Seq: 5}
			if tt.approve {
				want.Decision = api.DecisionApproved
			}
			require.Equal(t, want, res)

			var by, reason string
			if tt.approve {
				var p api.ApprovalGrantedPayload
				lastPayload(t, ledgerPath, api.ApprovalGranted, &p)
				by, reason = p.By, p.Reason
			} else {
				var p api.ApprovalDeniedPayload
				lastPayload(t, ledgerPath, api.ApprovalDenied, &p)
				by, reason = p.By, p.Reason
			}
			require.Equal(t, tt.wantBy, by)
			require.Equal(t, tt.wantReason, reason)
		})
	}
}

func TestReapproveIsIdempotent(t *testing.T) {
	// A front door retries on timeouts: repeating a decision must succeed and
	// append nothing, while contradicting one stays an error.
	tests := []struct {
		name        string
		first       bool // approve (true) or reject (false) first
		second      bool
		wantErr     string
		wantAlready bool
	}{
		{name: "approve twice", first: true, second: true, wantAlready: true},
		{name: "reject twice", first: false, second: false, wantAlready: true},
		{name: "reject after approve", first: true, second: false, wantErr: "was already approved; it cannot be rejected"},
		{name: "approve after reject", first: false, second: true, wantErr: "was already rejected; it cannot be approved"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, ledgerPath := bareWorkspace(t)
			ticket := parkTicket(t, ledgerPath)

			first, err := decide(t, root, ticket, tt.first)
			require.NoError(t, err)
			require.False(t, first.AlreadyDecided)
			decisions := countLogEvents(t, ledgerPath, api.ApprovalGranted) + countLogEvents(t, ledgerPath, api.ApprovalDenied)
			require.Equal(t, 1, decisions)

			second, err := decide(t, root, ticket, tt.second)
			after := countLogEvents(t, ledgerPath, api.ApprovalGranted) + countLogEvents(t, ledgerPath, api.ApprovalDenied)
			require.Equal(t, 1, after, "the second decision must append nothing")
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.True(t, second.AlreadyDecided)
			require.Equal(t, first.Decision, second.Decision)
			require.Equal(t, first.Seq, second.Seq, "a repeated decision reports the original's seq")
		})
	}
}

func TestDecideRejectsUndecidableTickets(t *testing.T) {
	tests := []struct {
		name    string
		ticket  string
		wantErr string
	}{
		{name: "unknown ticket", ticket: "g1-nope", wantErr: `unknown ticket "g1-nope"`},
		{name: "never parked", ticket: "g1-other", wantErr: `ticket "g1-other" is pending, not awaiting approval`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, ledgerPath := bareWorkspace(t)
			parkTicket(t, ledgerPath)
			led, err := ledger.Open(ledgerPath)
			require.NoError(t, err)
			e, err := api.NewEvent(api.TicketCreated, "test", api.TicketCreatedPayload{TicketID: "g1-other", GoalID: "g1", Title: "other"})
			require.NoError(t, err)
			_, err = led.Append(e)
			require.NoError(t, err)

			for _, approve := range []bool{true, false} {
				_, err := decide(t, root, tt.ticket, approve)
				require.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestSubmitGoalRegeneratesACollidingID(t *testing.T) {
	// A second Goal given an id already on the log would be dropped on replay
	// without a word; submitGoal must pick another id instead.
	led, err := ledger.Open(filepath.Join(t.TempDir(), "events.jsonl"))
	require.NoError(t, err)
	ids := []string{"g-taken", "g-taken", "g-taken", "g-fresh"}
	prev := newGoalID
	t.Cleanup(func() { newGoalID = prev })
	newGoalID = func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	}

	first, err := submitGoal(led, goalRequest{Text: "first", Source: "human"})
	require.NoError(t, err)
	require.Equal(t, "g-taken", first.GoalID)
	second, err := submitGoal(led, goalRequest{Text: "second", Source: "human"})
	require.NoError(t, err)
	require.Equal(t, "g-fresh", second.GoalID)

	events, err := led.Read()
	require.NoError(t, err)
	s, err := state.Fold(events)
	require.NoError(t, err)
	require.Len(t, s.Goals, 2)
	require.Equal(t, "second", s.Goals["g-fresh"].Text)
}

func TestCancelJSONIdempotent(t *testing.T) {
	root, ledgerPath := bareWorkspace(t)
	led, err := ledger.Open(ledgerPath)
	require.NoError(t, err)
	live, err := submitGoal(led, goalRequest{Text: "fix the flaky test", Source: "linear"})
	require.NoError(t, err)
	// g-merged settled with its work landed, g-failed with its only task failed.
	for _, ev := range []struct {
		typ     api.EventType
		payload any
	}{
		{api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g-merged", Text: "done"}},
		{api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-merged-impl", GoalID: "g-merged", Title: "impl"}},
		{api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g-merged-impl", Worker: "w1", Branch: "aoa/x"}},
		{api.Merged, api.MergedPayload{TicketID: "g-merged-impl", Worker: "w1", Commit: "c0ffee"}},
		{api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g-failed", Text: "doomed"}},
		{api.TicketCreated, api.TicketCreatedPayload{TicketID: "g-failed-impl", GoalID: "g-failed", Title: "impl"}},
		{api.TicketFailed, api.TicketFailedPayload{TicketID: "g-failed-impl", Reason: "gate failed"}},
	} {
		e, err := api.NewEvent(ev.typ, "test", ev.payload)
		require.NoError(t, err)
		_, err = led.Append(e)
		require.NoError(t, err)
	}

	// Steps run in order against one workspace.
	steps := []struct {
		name        string
		goalID      string
		wantErr     string
		wantAlready bool
		wantSeq     int
	}{
		{name: "first cancel", goalID: live.GoalID, wantSeq: 9},
		{name: "cancel again", goalID: live.GoalID, wantAlready: true, wantSeq: 9},
		{name: "unknown goal", goalID: "g-nope", wantErr: `unknown goal "g-nope"`},
		{name: "merged goal", goalID: "g-merged", wantErr: `goal "g-merged" already settled as merged; nothing to cancel`},
		{name: "failed goal", goalID: "g-failed", wantErr: `goal "g-failed" already settled as failed; nothing to cancel`},
	}
	for _, st := range steps {
		t.Run(st.name, func(t *testing.T) {
			var err error
			out := captureStdout(t, func() {
				err = cmdCancel([]string{"--path", root, "--json", "--by", "linear-bot", "--reason", "issue closed", st.goalID})
			})
			if st.wantErr != "" {
				require.ErrorContains(t, err, st.wantErr)
				require.Empty(t, out)
			} else {
				require.NoError(t, err)
				var res api.CancelResult
				decodeJSONLine(t, out, &res)
				require.Equal(t, api.CancelResult{Schema: api.ContractVersion, GoalID: st.goalID, Seq: st.wantSeq, AlreadyCancelled: st.wantAlready}, res)
			}
			require.Equal(t, 1, countLogEvents(t, ledgerPath, api.GoalCancelled), "exactly one cancel on the log")
		})
	}

	var p api.GoalCancelledPayload
	lastPayload(t, ledgerPath, api.GoalCancelled, &p)
	require.Equal(t, api.GoalCancelledPayload{GoalID: live.GoalID, By: "linear-bot", Reason: "issue closed"}, p)

	// Without --json both outcomes say what happened.
	other, err := submitGoal(led, goalRequest{Text: "another", Source: "human"})
	require.NoError(t, err)
	for _, want := range []string{"cancelled goal %s\n", "goal %s already cancelled\n"} {
		out := captureStdout(t, func() { err = cmdCancel([]string{"--path", root, other.GoalID}) })
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf(want, other.GoalID), out)
	}
}
