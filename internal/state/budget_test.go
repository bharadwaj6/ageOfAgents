package state

import (
	"testing"
	"time"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// at appends an event with an explicit timestamp, for day windows.
func (b *build) at(ts time.Time, typ api.EventType, payload any) *build {
	b.t.Helper()
	b.add(typ, payload)
	b.events[len(b.events)-1].Timestamp = ts
	return b
}

// A run's window is the events after it started; a day's is the events whose
// timestamp falls on that UTC day. A Goal counts in the window its first task
// was created in, and only once.
func TestUsageWindows(t *testing.T) {
	d1 := time.Date(2026, 9, 19, 23, 30, 0, 0, time.UTC)
	d2 := d1.Add(time.Hour) // 00:30 the next UTC day
	b := newBuild(t).
		at(d1, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "a"}).
		at(d1, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g1-impl", GoalID: "g1", Title: "a"}).
		at(d1, api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g1-impl", Worker: "w", Tokens: 100, Model: "m", CostUSD: 0.5}).
		// seq 4 onwards: a later run, on the next day
		at(d2, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g1-impl/a", GoalID: "g1", Title: "child"}).
		at(d2, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g2", Text: "b"}).
		at(d2, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g2-impl", GoalID: "g2", Title: "b"}).
		at(d2, api.TicketFailed, api.TicketFailedPayload{TicketID: "g2-impl", Reason: "x", Tokens: 40, Model: "m", CostUSD: 0.25})
	tests := []struct {
		name       string
		usage      Usage
		wantTokens int
		wantCost   float64
		wantGoals  int
	}{
		{name: "whole log", usage: RunUsage(b.events, 0), wantTokens: 140, wantCost: 0.75, wantGoals: 2},
		{name: "run since seq 3", usage: RunUsage(b.events, 3), wantTokens: 40, wantCost: 0.25, wantGoals: 1},
		{name: "day 1", usage: DayUsage(b.events, "2026-09-19"), wantTokens: 100, wantCost: 0.5, wantGoals: 1},
		{name: "day 2", usage: DayUsage(b.events, Day(d2)), wantTokens: 40, wantCost: 0.25, wantGoals: 1},
		{name: "an empty day", usage: DayUsage(b.events, "2026-09-21")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if u := tt.usage; u.TokensSpent != tt.wantTokens || u.CostUSD(nil) != tt.wantCost || u.Goals != tt.wantGoals {
				t.Errorf("usage = (%d tokens, $%v, %d goals), want (%d, $%v, %d)",
					u.TokensSpent, u.CostUSD(nil), u.Goals, tt.wantTokens, tt.wantCost, tt.wantGoals)
			}
		})
	}
}

// A spend limit holds back every new attempt and Goal; a Goal limit holds
// back only new Goals. A zero limit is no limit.
func TestBudgetReached(t *testing.T) {
	u := Usage{Spend: Spend{TokensSpent: 1000, CostUSDReported: 1}, Goals: 2}
	tests := []struct {
		name      string
		budget    Budget
		wantSpend bool
		wantGoals bool
	}{
		{name: "no limits", budget: Budget{}},
		{name: "usd under", budget: Budget{USD: 1.5}},
		{name: "usd reached", budget: Budget{USD: 1}, wantSpend: true},
		{name: "tokens reached", budget: Budget{Tokens: 1000}, wantSpend: true},
		{name: "goals under", budget: Budget{Goals: 3}},
		{name: "goals reached", budget: Budget{Goals: 2}, wantGoals: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.budget.SpendReached(u, nil); got != tt.wantSpend {
				t.Errorf("SpendReached = %v, want %v", got, tt.wantSpend)
			}
			if got := tt.budget.GoalsReached(u); got != tt.wantGoals {
				t.Errorf("GoalsReached = %v, want %v", got, tt.wantGoals)
			}
		})
	}
}

// A log holding BudgetExhausted replays, and records which window it closed.
func TestBudgetExhaustedReplays(t *testing.T) {
	s := newBuild(t).
		add(api.BudgetExhausted, api.BudgetExhaustedPayload{Scope: api.BudgetScopeRun, SpentUSD: 1, LimitUSD: 1}).
		add(api.BudgetExhausted, api.BudgetExhaustedPayload{Scope: api.BudgetScopeDay, Day: "2026-09-20", Goals: 3, LimitGoals: 3}).
		fold()
	if got := s.BudgetsExhausted; got[api.BudgetScopeRun] != 1 || got[BudgetWindow(api.BudgetScopeDay, "2026-09-20")] != 2 || len(got) != 2 {
		t.Errorf("BudgetsExhausted = %v, want run at seq 1 and day 2026-09-20 at seq 2", got)
	}
}
