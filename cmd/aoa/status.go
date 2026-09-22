package main

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/config"
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
// Spend — tokens and cost, per task, per Goal and in total — is replay's own
// (state.Spend), the very figures the spend governor enforces, so status and
// the governor cannot disagree. A Goal's spend is the sum of its tasks' and the
// totals are the sum of everything. Cost is what the harnesses reported, plus
// pricing (USD per million tokens, by model) for the spend they did not.
//
// Goals are ordered by submission (the seq of their GoalSubmitted event, then
// id), and each Goal's tasks by creation, so a decomposed task comes before its
// children. A task whose Goal is not on the log belongs to no GoalView; it is
// still counted in the totals.
//
// It returns the replayed state alongside the view: the view is the JSON
// contract, and callers that need something the contract does not carry (the
// seq a task failed at, for failedSince) read it from the state rather than
// folding the log a second time.
//
// The replay is done event by event rather than with state.Fold, because each
// Goal condition's last_transition_time is the moment its status last changed —
// history a final snapshot does not keep. After each event the conditions of
// the Goal it moved are recomputed and restamped.
func statusView(events []api.Event, pricing map[string]float64, day state.Budget) (api.StatusView, *state.State, error) {
	s := state.New()
	conditions := map[string][]api.Condition{}
	for _, e := range events {
		if err := s.Apply(e); err != nil {
			return api.StatusView{}, nil, fmt.Errorf("replay event log: %w", err)
		}
		for _, id := range goalsMoved(s, e) {
			conditions[id] = restamp(conditions[id], s.GoalConditions(id), e.Timestamp)
		}
	}
	m := metrics.Compute(events)

	graph := map[string]metrics.GraphShape{}
	for _, gs := range metrics.GraphShapes(s) {
		graph[gs.GoalID] = gs
	}
	tickets := map[string][]api.TicketView{}
	for _, id := range s.TicketOrder {
		if t := s.Tickets[id]; t != nil {
			tickets[t.GoalID] = append(tickets[t.GoalID], ticketView(t))
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
			Tokens:      s.Spend.TokensSpent,
			CostUSD:     s.Spend.CostUSD(pricing),
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
			Tokens:         g.TokensSpent,
			CostUSD:        g.CostUSD(pricing),
			Amendments:     g.Amendments,
			Branch:         g.Branch,
			PRURL:          g.PRURL,
			DeliveryError:  g.DeliveryError,
			Graph:          api.GraphView{MaxDepth: graph[g.ID].MaxDepth, MaxFanOut: graph[g.ID].MaxFanOut},
			Tickets:        tickets[g.ID],
			Conditions:     conditions[g.ID],
		}
		if gv.Tickets == nil {
			gv.Tickets = []api.TicketView{} // a queued Goal lists no tasks, not null
		}
		for _, tv := range gv.Tickets {
			if tv.Status == string(state.StatusMerged) {
				gv.Commits = append(gv.Commits, tv.Commit)
			}
		}
		if !goalDone(gv) {
			v.Settled = false
		}
		v.Goals = append(v.Goals, gv)
	}
	if !day.IsZero() {
		today := state.Day(time.Now())
		u := state.DayUsage(events, today)
		v.Budget = &api.BudgetView{
			Day:          today,
			USDSpent:     u.CostUSD(pricing),
			USDLimit:     day.USD,
			TokensSpent:  u.TokensSpent,
			TokensLimit:  day.Tokens,
			GoalsStarted: u.Goals,
			GoalsLimit:   day.Goals,
			Exhausted:    day.SpendReached(u, pricing) || day.GoalsReached(u),
		}
	}
	return v, s, nil
}

// ticketView is the contract's view of one task. Commit is carried only while
// it names something real — the candidate under review, or the merged commit —
// and the failure details only once the task has failed.
func ticketView(t *state.Ticket) api.TicketView {
	tv := api.TicketView{
		ID:       t.ID,
		Title:    t.Title,
		Status:   string(t.Status),
		Attempts: t.Attempts,
		Tokens:   t.TokensSpent,
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

// goalsMoved lists the Goals event e can have moved, so statusView recomputes
// only those: the Goal its payload names, else the Goal owning the task it
// names. A snapshot replaces the whole state, so it moves every Goal.
func goalsMoved(s *state.State, e api.Event) []string {
	if e.Type == api.StateSnapshot {
		ids := make([]string, 0, len(s.Goals))
		for id := range s.Goals {
			ids = append(ids, id)
		}
		return ids
	}
	if len(e.Payload) == 0 {
		return nil
	}
	var p struct {
		GoalID   string `json:"goal_id"`
		TicketID string `json:"ticket_id"`
	}
	if e.DecodePayload(&p) != nil {
		return nil
	}
	if p.GoalID != "" {
		return []string{p.GoalID}
	}
	if t := s.Tickets[p.TicketID]; t != nil {
		return []string{t.GoalID}
	}
	return nil
}

// restamp gives each condition in next the time its status last changed: the
// time prev recorded when the status is unchanged, else at — the event being
// replayed. A change of reason alone keeps the time, as in Kubernetes.
func restamp(prev, next []api.Condition, at time.Time) []api.Condition {
	for i := range next {
		next[i].LastTransitionTime = at
		for _, p := range prev {
			if p.Type == next[i].Type && p.Status == next[i].Status {
				next[i].LastTransitionTime = p.LastTransitionTime
			}
		}
	}
	return next
}

// goalDone reports whether gv's Complete condition is True: nothing more
// happens to the Goal without new input (ADR 020).
func goalDone(gv api.GoalView) bool {
	for _, c := range gv.Conditions {
		if c.Type == api.ConditionComplete {
			return c.Status == api.ConditionTrue
		}
	}
	return false
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
	if bv := v.Budget; bv != nil {
		fmt.Fprintf(&b, "budget %s: ", bv.Day)
		parts := []string{}
		if bv.USDLimit > 0 {
			parts = append(parts, fmt.Sprintf("$%.2f of $%.2f", bv.USDSpent, bv.USDLimit))
		}
		if bv.TokensLimit > 0 {
			parts = append(parts, fmt.Sprintf("%d of %d tokens", bv.TokensSpent, bv.TokensLimit))
		}
		if bv.GoalsLimit > 0 {
			parts = append(parts, fmt.Sprintf("%d of %d goals", bv.GoalsStarted, bv.GoalsLimit))
		}
		fmt.Fprint(&b, strings.Join(parts, ", "))
		if bv.Exhausted {
			fmt.Fprint(&b, " — exhausted; nothing new starts today")
		}
		fmt.Fprintln(&b)
	}
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
// failed after seq since, which is what makes `aoa run` exit non-zero. It always
// renders the whole workspace; since narrows only the count.
func printStatus(led *ledger.Ledger, pricing map[string]float64, day state.Budget, since int) (settled bool, failed int, err error) {
	events, err := led.Read()
	if err != nil {
		return false, 0, err
	}
	v, s, err := statusView(events, pricing, day)
	if err != nil {
		return false, 0, err
	}
	if err := renderStatus(os.Stdout, v); err != nil {
		return false, 0, err
	}
	return workSettled(v), failedSince(s, since), nil
}

// failedSince counts the tasks that are failed now and failed after seq since.
// A goal whose delivery failed and is still pending counts as one failure too,
// so a stuck delivery is alertable.
//
// The window is how `aoa run` tells its own failures from the workspace's
// history: a workspace outlives the run that failed in it, and counting every
// failure on the log made one old failure exit every later run non-zero forever
// (issue #156). since == 0 counts them all, which is what `aoa status` reports.
func failedSince(s *state.State, since int) int {
	failed := 0
	// A cancelled goal's tasks end failed, but a cancel is a front door's choice,
	// not something for `aoa run`'s exit status to alert on.
	live := func(goalID string) *state.Goal {
		if g := s.Goals[goalID]; g != nil && !g.Cancelled {
			return g
		}
		return nil
	}
	for _, g := range s.Goals {
		if live(g.ID) != nil && g.DeliveryError != "" && !g.Delivered && g.DeliveryFailedSeq > since {
			failed++
		}
	}
	for _, t := range s.Tickets {
		if live(t.GoalID) != nil && t.Status == state.StatusFailed && t.FailedSeq > since {
			failed++
		}
	}
	return failed
}

// dayBudget is the workspace's per-day budget, as the Scheduler counts it.
func dayBudget(cfg config.Config) state.Budget {
	return state.Budget{USD: cfg.Budget.USDPerDay, Tokens: cfg.Budget.TokensPerDay, Goals: cfg.Budget.GoalsPerDay}
}
