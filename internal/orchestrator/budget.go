package orchestrator

import (
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// budgetWindow is one budget and what its window has spent so far.
type budgetWindow struct {
	scope, day string
	budget     state.Budget
	usage      state.Usage
}

// budgetsReached reports what the run and day budgets hold back, and which
// windows have tripped (ADR 017). It only reads.
//
// spend true means no new attempt may be dispatched. goalsLeft is how many more
// Goals may be started: [budgetUnlimited] when nothing limits them, and 0 once a
// limit is reached. Reaching a spend limit allows no Goal either, since starting
// a Goal is what leads to spending. Attempts already running finish, so a window
// can end over its limit by at most one attempt per worker — which is why a
// harness's own per-attempt cap, and a small concurrency, are what keep the
// overshoot small.
func (o *Orchestrator) budgetsReached() (spend bool, goalsLeft int, tripped []budgetWindow, err error) {
	if o.opt.RunBudget.IsZero() && o.opt.DayBudget.IsZero() {
		return false, budgetUnlimited, nil, nil
	}
	events, err := o.led.Read()
	if err != nil {
		return false, 0, nil, err
	}
	goalsLeft = budgetUnlimited
	day := state.Day(o.opt.Now())
	for _, w := range []budgetWindow{
		{scope: api.BudgetScopeRun, budget: o.opt.RunBudget, usage: state.RunUsage(events, o.opt.RunSince)},
		{scope: api.BudgetScopeDay, day: day, budget: o.opt.DayBudget, usage: state.DayUsage(events, day)},
	} {
		if w.budget.IsZero() {
			continue
		}
		if w.budget.Goals > 0 {
			left := max(w.budget.Goals-w.usage.Goals, 0)
			if goalsLeft == budgetUnlimited || left < goalsLeft {
				goalsLeft = left
			}
		}
		spendReached := w.budget.SpendReached(w.usage, o.opt.Pricing)
		if spendReached {
			spend, goalsLeft = true, 0
		}
		if !spendReached && !w.budget.GoalsReached(w.usage) {
			continue
		}
		tripped = append(tripped, w)
	}
	return spend, goalsLeft, tripped, nil
}

// budgetUnlimited is goalsLeft when no budget limits how many Goals may start.
const budgetUnlimited = -1

// budgetStop is budgetsReached, plus one BudgetExhausted per window the log
// does not record yet. Passes after the first see the record and stay quiet.
func (o *Orchestrator) budgetStop(s *state.State) (spend bool, goalsLeft int, err error) {
	spend, goalsLeft, tripped, err := o.budgetsReached()
	if err != nil {
		return false, 0, err
	}
	for _, w := range tripped {
		if _, recorded := s.BudgetsExhausted[state.BudgetWindow(w.scope, w.day)]; recorded {
			continue
		}
		if err := o.emit(api.BudgetExhausted, api.BudgetExhaustedPayload{
			Scope:       w.scope,
			Day:         w.day,
			SpentUSD:    w.usage.CostUSD(o.opt.Pricing),
			LimitUSD:    w.budget.USD,
			SpentTokens: w.usage.TokensSpent,
			LimitTokens: w.budget.Tokens,
			Goals:       w.usage.Goals,
			LimitGoals:  w.budget.Goals,
		}); err != nil {
			return false, 0, err
		}
	}
	return spend, goalsLeft, nil
}

// budgetPaused reports whether a budget is the only reason the run has stopped
// making progress. Run treats that as a clean end, like work parked for
// approval: what is left is not stuck, it is unaffordable, and a later run
// under a fresh budget picks it up.
func (o *Orchestrator) budgetPaused() bool {
	spend, goalsLeft, _, err := o.budgetsReached()
	return err == nil && (spend || goalsLeft == 0)
}
