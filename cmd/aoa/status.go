package main

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/metrics"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// statusView projects the Event Log onto the snapshot `aoa status` reports. It
// is the one place status numbers are computed: `--json` prints the view as is,
// and the human text is rendered from it (renderStatus), so the two cannot
// disagree. It replays the log once into state and once through
// metrics.Compute; everything else is derived from those two.
//
// Tokens come from metrics.Compute throughout — per task, per Goal and in total
// — so a Goal's tokens are the sum of its tasks' and the totals are the sum of
// everything. Costs price those same tallies with pricing (USD per million
// tokens, by model).
//
// Goals are ordered by submission (the seq of their GoalSubmitted event, then
// id), and each Goal's tasks by creation, so a decomposed task comes before its
// children. A task whose Goal is not on the log belongs to no GoalView; it is
// still counted in the totals.
func statusView(events []api.Event, pricing map[string]float64) (api.StatusView, error) {
	s, err := state.Fold(events)
	if err != nil {
		return api.StatusView{}, fmt.Errorf("replay event log: %w", err)
	}
	m := metrics.Compute(events)

	ticketTokens := make(map[string]int, len(m.PerTicket))
	for _, tc := range m.PerTicket {
		ticketTokens[tc.TicketID] = tc.Tokens
	}
	goalCost := make(map[string]metrics.GoalCost, len(m.PerGoal))
	for _, gc := range m.PerGoal {
		goalCost[gc.GoalID] = gc
	}
	graph := map[string]metrics.GraphShape{}
	for _, gs := range metrics.GraphShapes(s) {
		graph[gs.GoalID] = gs
	}
	tickets := map[string][]api.TicketView{}
	for _, id := range s.TicketOrder {
		if t := s.Tickets[id]; t != nil {
			tickets[t.GoalID] = append(tickets[t.GoalID], ticketView(t, ticketTokens[id]))
		}
	}

	goals := make([]*state.Goal, 0, len(s.Goals))
	for _, g := range s.Goals {
		goals = append(goals, g)
	}
	slices.SortFunc(goals, func(a, b *state.Goal) int {
		return cmp.Or(cmp.Compare(a.SubmittedSeq, b.SubmittedSeq), cmp.Compare(a.ID, b.ID))
	})

	v := api.StatusView{
		Schema:  api.ContractVersion,
		LastSeq: s.LastSeq,
		Settled: true,
		Goals:   make([]api.GoalView, 0, len(goals)),
		Totals: api.StatusTotals{
			Tokens:      m.TokensTotal,
			CostUSD:     metrics.USD(m.TokensByModel, pricing),
			WallSeconds: m.DurationSeconds,
			Goals:       m.Goals,
			Tickets:     m.Tickets,
			Merged:      m.Merged,
			Failed:      m.Failed,
			Awaiting:    len(s.AwaitingApproval()),
		},
		MergeQueue: api.MergeQueueView{
			MaxDepth:        m.MergeQueueMaxDepth,
			WaitMeanSeconds: m.MergeQueueWaitMean,
			WaitMaxSeconds:  m.MergeQueueWaitMax,
		},
	}
	for _, g := range goals {
		gv := api.GoalView{
			ID:             g.ID,
			Text:           g.Text,
			Source:         g.Source,
			Ref:            g.Ref,
			By:             g.By,
			SubmittedAt:    g.SubmittedAt,
			Outcome:        s.GoalOutcome(g.ID),
			BudgetExceeded: g.BudgetExceeded,
			Tokens:         goalCost[g.ID].Tokens,
			CostUSD:        metrics.USD(goalCost[g.ID].TokensByModel, pricing),
			Amendments:     g.Amendments,
			Branch:         g.Branch,
			PRURL:          g.PRURL,
			DeliveryError:  g.DeliveryError,
			Graph:          api.GraphView{MaxDepth: graph[g.ID].MaxDepth, MaxFanOut: graph[g.ID].MaxFanOut},
			Tickets:        tickets[g.ID],
		}
		if gv.Tickets == nil {
			gv.Tickets = []api.TicketView{} // a queued Goal lists no tasks, not null
		}
		for _, tv := range gv.Tickets {
			if tv.Status == string(state.StatusMerged) {
				gv.Commits = append(gv.Commits, tv.Commit)
			}
		}
		switch gv.Outcome {
		case api.OutcomeMerged, api.OutcomeFailed, api.OutcomeDelivered:
		case api.OutcomeCancelled:
			// Settled once the Scheduler has nothing of it left to finish or fail.
			for _, tv := range gv.Tickets {
				if !state.TicketStatus(tv.Status).IsTerminal() {
					v.Settled = false
				}
			}
		default:
			v.Settled = false
		}
		v.Goals = append(v.Goals, gv)
	}
	return v, nil
}

// ticketView is the contract's view of one task. Commit is carried only while
// it names something real — the candidate under review, or the merged commit —
// and the failure details only once the task has failed.
func ticketView(t *state.Ticket, tokens int) api.TicketView {
	tv := api.TicketView{
		ID:       t.ID,
		Title:    t.Title,
		Status:   string(t.Status),
		Attempts: t.Attempts,
		Tokens:   tokens,
		Depth:    t.Depth,
	}
	switch t.Status {
	case state.StatusProposed, state.StatusAwaiting, state.StatusMerged:
		tv.Commit = t.Commit
	case state.StatusFailed:
		tv.FailReason = t.LastFailReason
		tv.Worktree = t.Worktree
		tv.Rejected = t.Rejected
	}
	return tv
}

// workSettled reports whether no task in v is still in flight. It is the text
// output's notion of settled, which prints "all work settled" and ends
// --watch. Unlike StatusView.Settled it does not wait for a queued Goal: the
// human output has always called a workspace settled before the Scheduler
// picked up a newly submitted Goal.
func workSettled(v api.StatusView) bool {
	for _, g := range v.Goals {
		for _, t := range g.Tickets {
			if !state.TicketStatus(t.Status).IsTerminal() {
				return false
			}
		}
	}
	return true
}

// renderStatus writes the human `aoa status` text for v: goals (with their pull
// request, or why delivery is pending, in PR delivery mode) and their graph
// shapes by id, every task by id, a "needs human" handoff for each failed task,
// then the totals. It formats the view and computes nothing of its own.
func renderStatus(w io.Writer, v api.StatusView) error {
	var b bytes.Buffer
	if len(v.Goals) == 0 {
		b.WriteString("no goals submitted\n")
		return writeAll(w, b.Bytes())
	}

	goals := slices.Clone(v.Goals)
	slices.SortFunc(goals, func(a, b api.GoalView) int { return cmp.Compare(a.ID, b.ID) })
	var tickets []api.TicketView
	for _, g := range goals {
		fmt.Fprintf(&b, "goal %s: %s\n", g.ID, g.Text)
		if g.PRURL != "" {
			fmt.Fprintf(&b, "  pr: %s\n", g.PRURL)
		}
		if g.DeliveryError != "" {
			// git's error output runs to several lines; keep them under the goal.
			fmt.Fprintf(&b, "  delivery pending: %s\n", strings.ReplaceAll(g.DeliveryError, "\n", "\n    "))
		}
		tickets = append(tickets, g.Tickets...)
	}
	for _, g := range goals {
		if len(g.Tickets) > 0 {
			fmt.Fprintf(&b, "  graph %s: tickets=%d depth=%d fan-out=%d\n",
				g.ID, len(g.Tickets), g.Graph.MaxDepth, g.Graph.MaxFanOut)
		}
	}
	b.WriteString("\n")

	slices.SortFunc(tickets, func(a, b api.TicketView) int { return cmp.Compare(a.ID, b.ID) })
	var needsHuman []api.TicketView
	for _, t := range tickets {
		fmt.Fprintf(&b, "  [%-8s] %s  (attempts=%d tokens=%d)\n", t.Status, t.ID, t.Attempts, t.Tokens)
		if t.Status == string(state.StatusFailed) {
			needsHuman = append(needsHuman, t)
		}
	}

	// Warm handoff: list terminally-failed tickets with why they failed and the
	// preserved worktree to take over (when one was kept).
	if len(needsHuman) > 0 {
		b.WriteString("\nneeds human — failed tickets:\n")
		for _, t := range needsHuman {
			reason := t.FailReason
			if reason == "" {
				reason = "(no reason recorded)"
			}
			fmt.Fprintf(&b, "  %s — %s\n", t.ID, reason)
			if t.Worktree != "" {
				fmt.Fprintf(&b, "      take over: cd %s\n", t.Worktree)
			}
		}
	}

	fmt.Fprintf(&b, "\ntotal: tokens=%d  wall=%.1fs", v.Totals.Tokens, v.Totals.WallSeconds)
	if v.Totals.CostUSD > 0 {
		fmt.Fprintf(&b, "  cost=$%.4f", v.Totals.CostUSD)
	}
	b.WriteString("\n")
	if v.MergeQueue.MaxDepth > 0 {
		fmt.Fprintf(&b, "merge queue: max-depth=%d  wait-mean=%.1fs  wait-max=%.1fs\n",
			v.MergeQueue.MaxDepth, v.MergeQueue.WaitMeanSeconds, v.MergeQueue.WaitMaxSeconds)
	}
	if workSettled(v) {
		b.WriteString("all work settled\n")
	}
	return writeAll(w, b.Bytes())
}

// writeAll writes p to w in one call, so a failed write is reported rather
// than dropped.
func writeAll(w io.Writer, p []byte) error {
	if _, err := w.Write(p); err != nil {
		return fmt.Errorf("write status: %w", err)
	}
	return nil
}

// printStatus renders the run's live state to stdout and reports whether all
// work has settled (the signal --watch uses to stop polling) and how many tasks
// failed (which makes `aoa run` exit non-zero). A goal whose delivery failed and
// is still pending counts as one failure, so a stuck delivery is alertable.
func printStatus(led *ledger.Ledger, pricing map[string]float64) (settled bool, failed int, err error) {
	events, err := led.Read()
	if err != nil {
		return false, 0, err
	}
	v, err := statusView(events, pricing)
	if err != nil {
		return false, 0, err
	}
	if err := renderStatus(os.Stdout, v); err != nil {
		return false, 0, err
	}
	// A cancelled goal's tasks end failed, but a cancel is a front door's
	// choice, not something for `aoa run`'s exit status to alert on.
	for _, g := range v.Goals {
		if g.Outcome == api.OutcomeCancelled {
			continue
		}
		if g.DeliveryError != "" && g.Outcome != api.OutcomeDelivered {
			failed++
		}
		for _, t := range g.Tickets {
			if t.Status == string(state.StatusFailed) {
				failed++
			}
		}
	}
	return workSettled(v), failed, nil
}
