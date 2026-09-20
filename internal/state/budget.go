package state

import (
	"time"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Day is the UTC calendar day t falls on, as day budgets count days
// ("2006-01-02").
func Day(t time.Time) string { return t.UTC().Format(time.DateOnly) }

// Usage is what a window of the Event Log spent, and how many Goals it started.
type Usage struct {
	Spend
	Goals int // Goals whose first task was created in the window
}

// RunUsage is the usage of a run: the events after seq since, the last event
// on the log when the run started.
func RunUsage(events []api.Event, since int) Usage {
	return usageOf(events, func(e api.Event) bool { return e.Seq > since })
}

// DayUsage is the usage of one UTC day (see [Day]), by event timestamp.
func DayUsage(events []api.Event, day string) Usage {
	return usageOf(events, func(e api.Event) bool { return Day(e.Timestamp) == day })
}

// usageOf sums the spend and Goal starts of the events in selects. Spend is
// read through ChargeOf, as replay reads it. A Goal starts when its first task
// is created, so a Goal still queued has started nothing and costs nothing.
func usageOf(events []api.Event, in func(api.Event) bool) Usage {
	var u Usage
	started := map[string]bool{}
	for _, e := range events {
		if e.Type == api.TicketCreated {
			var p api.TicketCreatedPayload
			if e.DecodePayload(&p) == nil && !started[p.GoalID] {
				started[p.GoalID] = true
				if in(e) {
					u.Goals++
				}
			}
		}
		if _, c, ok := ChargeOf(e); ok && in(e) {
			u.Add(c)
		}
	}
	return u
}

// Budget limits a window's spend and Goal starts (ADR 017). A zero field is no
// limit.
type Budget struct {
	USD    float64 // dollars, as Spend.CostUSD counts them
	Tokens int
	Goals  int // Goals started
}

// IsZero reports whether b limits nothing.
func (b Budget) IsZero() bool { return b == Budget{} }

// SpendReached reports whether u has reached b's dollar or token limit, pricing
// with pricing. Past it no new attempt may start, and no new Goal.
func (b Budget) SpendReached(u Usage, pricing map[string]float64) bool {
	return (b.USD > 0 && u.CostUSD(pricing) >= b.USD) || (b.Tokens > 0 && u.TokensSpent >= b.Tokens)
}

// GoalsReached reports whether u has started as many Goals as b allows. Past it
// no new Goal may start; Goals already started carry on.
func (b Budget) GoalsReached(u Usage) bool {
	return b.Goals > 0 && u.Goals >= b.Goals
}

// BudgetWindow names the window a BudgetExhausted closed, as
// State.BudgetsExhausted keys it: the scope, and for a day budget the day.
func BudgetWindow(scope, day string) string {
	if scope == api.BudgetScopeDay {
		return scope + ":" + day
	}
	return scope
}
