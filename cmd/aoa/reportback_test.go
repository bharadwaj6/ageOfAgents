package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// reportable is what a front door tells the requester about: the events that
// change what someone waiting on a Goal needs to know. aoa emits no
// notification of its own (ADR 019), so everything a reporter needs must be
// derivable from the contract.
var reportable = map[api.EventType]bool{
	api.Merged:             true,
	api.TicketFailed:       true,
	api.ApprovalRequested:  true,
	api.GoalBudgetExceeded: true,
	api.GoalCancelled:      true,
	api.Delivered:          true,
	api.DeliveryFailed:     true,
}

// decodeEventLines parses `aoa events --json` output into events.
func decodeEventLines(t *testing.T, out string) []api.Event {
	t.Helper()
	var events []api.Event
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		var e api.Event
		require.NoError(t, json.Unmarshal([]byte(line), &e), "output line is not an event: %q", line)
		events = append(events, e)
	}
	return events
}

// goalOf resolves the Goal an event belongs to the way a reporter must: from
// the event's own payload when it names a Goal, and otherwise by looking its
// task up in the snapshot. Nothing else joins a task to its origin.
func goalOf(t *testing.T, e api.Event, view api.StatusView) api.GoalView {
	t.Helper()
	var p struct {
		GoalID string `json:"goal_id"`
	}
	if err := e.DecodePayload(&p); err == nil && p.GoalID != "" {
		for _, g := range view.Goals {
			if g.ID == p.GoalID {
				return g
			}
		}
	}
	if id := e.TicketID(); id != "" {
		for _, g := range view.Goals {
			for _, tk := range g.Tickets {
				if tk.ID == id {
					return g
				}
			}
		}
	}
	t.Fatalf("no Goal in `aoa status --json` owns %s (seq %d): a reporter cannot tell anyone about it", e.Type, e.Seq)
	return api.GoalView{}
}

// statusJSON runs `aoa status --path root --json` and decodes it.
func statusJSON(t *testing.T, root string) api.StatusView {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = cmdStatus([]string{"--path", root, "--json"}) })
	require.NoError(t, err, "aoa status --json")
	var view api.StatusView
	decodeJSONLine(t, out, &view)
	return view
}

// Reporting an outcome back to where the work came from belongs to the front
// door, not to aoa (ADR 019). This is the conformance test for the recipe
// docs/backend.md prescribes in place of an outbound notifier: follow the log
// from a cursor the caller holds, and join what it finds to a Goal with one
// snapshot.
//
// It uses nothing but `aoa events --json --since` and `aoa status --json`, so
// it fails if either half of that join stops being derivable — the event's task
// no longer resolving to a Goal, or the Goal no longer carrying the origin the
// submitter gave and the outcome to report — or if the cursor stops being
// exclusive, which would report the same thing twice.
func TestReportingBackNeedsOnlyTheContract(t *testing.T) {
	root, _ := statusWorkspace(t, mixedLog(t))

	// What a reporter must be able to say about each Goal that has news: where
	// to say it, and what the news is. g-queued has no reportable event yet.
	// done is whether that news is final: a reporter reads it off the Complete
	// condition (ADR 020) rather than enumerating which outcomes are terminal.
	want := map[string]struct {
		ref, outcome string
		done         bool
	}{
		"g-merged": {"", api.OutcomeMerged, true},
		"g-split":  {"https://linear.app/x/ENG-7", api.OutcomeMerged, true},
		"g-failed": {"https://github.com/o/r/issues/3", api.OutcomeFailed, true},
		"g-await":  {"", api.OutcomeAwaitingApproval, false},
	}

	// 1. Everything that happened since the cursor, which the caller holds.
	events := decodeEventLines(t, runEvents(t, "--path", root, "--json", "--since", "0"))
	// 2. One snapshot says who to tell and what the Goal's outcome now is.
	view := statusJSON(t, root)

	reported := map[string]bool{}
	for _, e := range events {
		if !reportable[e.Type] {
			continue
		}
		g := goalOf(t, e, view)
		w, ok := want[g.ID]
		require.True(t, ok, "%s (seq %d) resolved to unexpected Goal %s", e.Type, e.Seq, g.ID)
		require.Equal(t, w.ref, g.Ref, "Goal %s: a reporter sends its news to ref", g.ID)
		require.Equal(t, w.outcome, g.Outcome, "Goal %s: outcome to report", g.ID)
		require.Equal(t, w.done, goalDone(g), "Goal %s: whether its news is final (Complete)", g.ID)
		reported[g.ID] = true
	}
	require.Len(t, reported, len(want), "every Goal with news must be reachable from the event stream, got %v", reported)

	// 3. The cursor is the caller's, and it resumes exactly: nothing already
	// reported comes round again, and the next thing that happens is not missed.
	cursor := strconv.Itoa(view.LastSeq)
	require.Empty(t, runEvents(t, "--path", root, "--json", "--since", cursor),
		"a reporter resuming at last_seq would report events it already sent")

	ws, err := workspaceAt(root)
	require.NoError(t, err)
	led, err := ledger.Open(ws.ledgerPath)
	require.NoError(t, err)
	merged, err := api.NewEvent(api.Merged, "test", api.MergedPayload{TicketID: "g-await-impl", Worker: "w7", Commit: "c0ffee9"})
	require.NoError(t, err)
	_, err = led.Append(merged)
	require.NoError(t, err)

	next := decodeEventLines(t, runEvents(t, "--path", root, "--json", "--since", cursor))
	require.Len(t, next, 1, "resuming at last_seq must yield exactly what was appended since")
	require.Equal(t, api.Merged, next[0].Type)
	require.Equal(t, view.LastSeq+1, next[0].Seq)

	// And the news is the Goal's new outcome, from the same snapshot verb.
	after := statusJSON(t, root)
	require.Equal(t, api.OutcomeMerged, goalOf(t, next[0], after).Outcome)
	require.Greater(t, after.LastSeq, view.LastSeq, "the snapshot carries the cursor to resume from next time")
}
