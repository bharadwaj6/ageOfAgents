package orchestrator

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// costBackend does the mock's work and reports a fixed cost per attempt, the
// way a harness that prices its own calls does (claude's total_cost_usd).
type costBackend struct {
	inner agent.Backend
	usd   float64
}

func (*costBackend) Name() string { return "cost" }

func (c *costBackend) Run(ctx context.Context, task agent.Task) (agent.Result, error) {
	res, err := c.inner.Run(ctx, task)
	res.CostUSD = c.usd
	res.Tokens += 1000
	res.Model = "test-model"
	return res, err
}

// budgetEvents returns the BudgetExhausted payloads on the log.
func budgetEvents(t *testing.T, h *harness) []api.BudgetExhaustedPayload {
	t.Helper()
	events, err := h.led.Read()
	require.NoError(t, err)
	var out []api.BudgetExhaustedPayload
	for _, e := range events {
		if e.Type != api.BudgetExhausted {
			continue
		}
		var p api.BudgetExhaustedPayload
		require.NoError(t, e.DecodePayload(&p))
		out = append(out, p)
	}
	return out
}

// countEvents counts the events of one type on the log.
func countEvents(t *testing.T, h *harness, typ api.EventType) int {
	t.Helper()
	events, err := h.led.Read()
	require.NoError(t, err)
	n := 0
	for _, e := range events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// startedGoals counts the Goals that have a task: a Goal a budget held back
// never gets one, and so never spends.
func startedGoals(t *testing.T, h *harness) int {
	t.Helper()
	s := h.state(t)
	started := map[string]bool{}
	for _, tk := range s.Tickets {
		started[tk.GoalID] = true
	}
	return len(started)
}

// A run budget bounds one `aoa run`: past it no new attempt is dispatched and
// no new Goal is started, the overshoot is at most one attempt per worker, and
// the stop is recorded once rather than every pass.
func TestRunBudgetStopsNewWork(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, h := setup(t, &costBackend{inner: agent.NewMock(), usd: 0.40}, pass, Options{
		Concurrency: 1,
		RunBudget:   state.Budget{USD: 1.00},
	})
	for _, id := range []string{"g-1", "g-2", "g-3", "g-4", "g-5"} {
		h.submitGoal(t, id, "add "+id)
	}
	require.NoError(t, o.Run(context.Background()))

	trips := budgetEvents(t, h)
	require.Len(t, trips, 1, "one BudgetExhausted for the run, however many passes see it")
	require.Equal(t, api.BudgetScopeRun, trips[0].Scope)
	require.Equal(t, 1.00, trips[0].LimitUSD)

	events, err := h.led.Read()
	require.NoError(t, err)
	spent := state.RunUsage(events, 0).CostUSD(nil)
	require.GreaterOrEqual(t, spent, 1.00, "the run only stops once it has reached the limit")
	require.LessOrEqual(t, spent, 1.00+0.40, "overshoot is at most concurrency x one attempt's cost")
	// Three attempts fit: the fourth would be dispatched only if $1.20 were
	// still under the limit. The work left over was never attempted, so it
	// cost nothing.
	require.Equal(t, 3, countEvents(t, h, api.WorkStarted), "attempts stop once the run has spent its budget")
}

// A day budget is the workspace's, not one run's: it is counted from event
// timestamps, so it survives across runs and resets when the UTC day does.
func TestDayBudgetCountsGoalsAndResetsNextDay(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	day1 := time.Date(2026, 9, 20, 23, 0, 0, 0, time.UTC)
	now := day1
	o, h := setup(t, agent.NewMock(), pass, Options{
		Concurrency: 1,
		DayBudget:   state.Budget{Goals: 1},
		Now:         func() time.Time { return now },
	})
	h.submitGoal(t, "g-1", "first")
	h.submitGoal(t, "g-2", "second")
	require.NoError(t, o.Run(context.Background()))

	require.Equal(t, 1, startedGoals(t, h), "the second Goal waits for tomorrow's budget")
	trips := budgetEvents(t, h)
	require.Len(t, trips, 1)
	require.Equal(t, api.BudgetScopeDay, trips[0].Scope)
	require.Equal(t, state.Day(day1), trips[0].Day)
	require.Equal(t, 1, trips[0].LimitGoals)

	// The next UTC day is a fresh window, and the same workspace carries on.
	now = day1.Add(2 * time.Hour)
	require.NoError(t, o.Run(context.Background()))
	require.Equal(t, 2, startedGoals(t, h), "a new day starts the Goal that was held back")
	require.Len(t, budgetEvents(t, h), 1, "the new day has not tripped anything")
}

// A budget already spent is not an error: the next run under it ends cleanly,
// and the work that is left is simply unaffordable until the next window.
func TestRunUnderAnExhaustedBudgetEndsCleanly(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, h := setup(t, &costBackend{inner: agent.NewMock(), usd: 0.40}, pass, Options{
		Concurrency: 1,
		RunBudget:   state.Budget{USD: 0.10}, // one attempt spends four times this
	})
	h.submitGoal(t, "g-1", "add a greeting")
	require.NoError(t, o.Run(context.Background()))
	require.Equal(t, 1, startedGoals(t, h))

	// The window is the whole log here, so the budget is spent when the next
	// run starts: it must start nothing and still end cleanly.
	h.submitGoal(t, "g-2", "add a farewell")
	require.NoError(t, o.Run(context.Background()), "an exhausted budget is a clean stop, not a stall")
	require.Equal(t, 1, startedGoals(t, h), "the second Goal was never started, so it cost nothing")
	require.Len(t, budgetEvents(t, h), 1, "the run window is recorded once")
}
