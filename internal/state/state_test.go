package state

import (
	"testing"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// build is a tiny helper to construct a sequenced event stream for folding.
type build struct {
	t      *testing.T
	events []api.Event
	seq    int
}

func newBuild(t *testing.T) *build { return &build{t: t} }

func (b *build) add(typ api.EventType, payload any) *build {
	b.t.Helper()
	e, err := api.NewEvent(typ, "test", payload)
	if err != nil {
		b.t.Fatalf("NewEvent: %v", err)
	}
	b.seq++
	e.Seq = b.seq
	b.events = append(b.events, e)
	return b
}

func (b *build) fold() *State {
	b.t.Helper()
	s, err := Fold(b.events)
	if err != nil {
		b.t.Fatalf("Fold: %v", err)
	}
	return s
}

func TestHappyPathFold(t *testing.T) {
	s := newBuild(t).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "do it"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "impl", IdempotencyKey: "k1"}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "t1"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "t1", Worker: "w1"}).
		add(api.WorkStarted, api.WorkStartedPayload{TicketID: "t1", Worker: "w1", Worktree: "/wt"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "t1", Worker: "w1", Branch: "aoa/t1", Commit: "c1"}).
		add(api.VerificationPassed, api.VerificationPassedPayload{TicketID: "t1", Worker: "w1"}).
		add(api.Merged, api.MergedPayload{TicketID: "t1", Worker: "w1", Commit: "c1"}).
		fold()

	tk := s.Tickets["t1"]
	if tk == nil {
		t.Fatal("ticket t1 missing")
	}
	if tk.Status != StatusMerged {
		t.Errorf("status = %s, want merged", tk.Status)
	}
	if tk.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", tk.Attempts)
	}
	if tk.Commit != "c1" {
		t.Errorf("commit = %q, want c1", tk.Commit)
	}
	if !s.Settled() {
		t.Error("expected settled")
	}
	if s.LastSeq != 8 {
		t.Errorf("LastSeq = %d, want 8", s.LastSeq)
	}
}

func TestIdempotentTicketCreation(t *testing.T) {
	s := newBuild(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "same"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t2", Title: "x-dup", IdempotencyKey: "same"}).
		fold()

	if len(s.Tickets) != 1 {
		t.Fatalf("got %d tickets, want 1 (idempotency key should dedupe)", len(s.Tickets))
	}
	if _, ok := s.Tickets["t1"]; !ok {
		t.Error("first ticket should win")
	}
	if _, ok := s.Tickets["t2"]; ok {
		t.Error("duplicate ticket should be ignored")
	}
}

func TestDependencyReadiness(t *testing.T) {
	b := newBuild(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "plan", Title: "plan", IdempotencyKey: "kp"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "impl", Title: "impl", DependsOn: []string{"plan"}, IdempotencyKey: "ki"})
	s := b.fold()

	// Only the dependency-free ticket should be newly ready.
	ready := s.NewlyReady()
	if len(ready) != 1 || ready[0].ID != "plan" {
		t.Fatalf("NewlyReady = %v, want [plan]", ids(ready))
	}

	// Advance "plan" to merged, then "impl" becomes ready.
	s = b.
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "plan"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "plan", Worker: "w1"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "plan", Worker: "w1", Commit: "c"}).
		add(api.Merged, api.MergedPayload{TicketID: "plan", Worker: "w1", Commit: "c"}).
		fold()

	ready = s.NewlyReady()
	if len(ready) != 1 || ready[0].ID != "impl" {
		t.Fatalf("after plan merged, NewlyReady = %v, want [impl]", ids(ready))
	}
	if !s.DepsSatisfied(s.Tickets["impl"]) {
		t.Error("impl deps should be satisfied")
	}
}

func TestVerificationFailedRetriesTicket(t *testing.T) {
	s := newBuild(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", Title: "x", IdempotencyKey: "k"}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "t1"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "t1", Worker: "w1"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "t1", Worker: "w1", Commit: "c1"}).
		add(api.VerificationFailed, api.VerificationFailedPayload{TicketID: "t1", Worker: "w1", Reason: "tests failed"}).
		fold()

	tk := s.Tickets["t1"]
	if tk.Status != StatusReady {
		t.Errorf("status = %s, want ready (re-dispatchable)", tk.Status)
	}
	if tk.Worker != "" || tk.Commit != "" {
		t.Errorf("proposal fields should be cleared, got worker=%q commit=%q", tk.Worker, tk.Commit)
	}
	if s.Settled() {
		t.Error("should not be settled while a ticket is ready")
	}
	if len(s.ReadyTickets()) != 1 {
		t.Errorf("expected 1 ready ticket for retry")
	}
}

func TestActiveCountBackpressure(t *testing.T) {
	s := newBuild(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", Title: "a", IdempotencyKey: "k1"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t2", Title: "b", IdempotencyKey: "k2"}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "t1"}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "t2"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "t1", Worker: "w1"}).
		add(api.WorkStarted, api.WorkStartedPayload{TicketID: "t1", Worker: "w1"}).
		fold()

	if got := s.ActiveCount(); got != 1 {
		t.Errorf("ActiveCount = %d, want 1", got)
	}
	if got := len(s.ReadyTickets()); got != 1 {
		t.Errorf("ReadyTickets = %d, want 1 (t2 still ready)", got)
	}
}

func TestUnknownEventErrors(t *testing.T) {
	e, _ := api.NewEvent(api.EventType("Bogus"), "x", nil)
	if _, err := Fold([]api.Event{e}); err == nil {
		t.Error("expected error for unknown event type")
	}
}

func TestHasCycle(t *testing.T) {
	tests := []struct {
		name string
		adj  map[string][]string
		want bool
	}{
		{"empty", map[string][]string{}, false},
		{"linear", map[string][]string{"a": {"b"}, "b": {"c"}, "c": nil}, false},
		{"diamond", map[string][]string{"d": {"b", "c"}, "b": {"a"}, "c": {"a"}, "a": nil}, false},
		{"self-loop", map[string][]string{"a": {"a"}}, true},
		{"two-cycle", map[string][]string{"a": {"b"}, "b": {"a"}}, true},
		{"deep-cycle", map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"a"}}, true},
		{"target-only-node", map[string][]string{"a": {"b"}}, false}, // b has no out-edges
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasCycle(tt.adj); got != tt.want {
				t.Errorf("HasCycle(%v) = %v, want %v", tt.adj, got, tt.want)
			}
		})
	}
}

func TestWouldCycle(t *testing.T) {
	s := newBuild(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "a", Title: "a", IdempotencyKey: "ka"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "b", Title: "b", DependsOn: []string{"a"}, IdempotencyKey: "kb"}).
		fold()

	if s.WouldCycle(map[string][]string{"c": {"a"}}) {
		t.Error("adding c->a should not create a cycle")
	}
	if !s.WouldCycle(map[string][]string{"a": {"b"}}) {
		t.Error("adding a->b should create a cycle (b already depends on a)")
	}
}

func TestDecomposedParentIsTerminalAndCompletesWithChildren(t *testing.T) {
	b := newBuild(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "root", Title: "root", IdempotencyKey: "kr"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "dep", Title: "dep", DependsOn: []string{"root"}, IdempotencyKey: "kd"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "root/c1", Title: "c1", IdempotencyKey: "kc1", Depth: 1}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "root/c2", Title: "c2", IdempotencyKey: "kc2", Depth: 1}).
		add(api.TicketDecomposed, api.TicketDecomposedPayload{TicketID: "root", Children: []string{"root/c1", "root/c2"}})
	s := b.fold()

	if s.Tickets["root"].Status != StatusDecomposed {
		t.Fatalf("root status = %s, want decomposed", s.Tickets["root"].Status)
	}
	if !StatusDecomposed.IsTerminal() {
		t.Error("decomposed should be terminal")
	}
	if s.DepsSatisfied(s.Tickets["dep"]) {
		t.Error("dep must not be satisfied before the decomposed parent's children merge")
	}

	s = b.
		add(api.Merged, api.MergedPayload{TicketID: "root/c1", Commit: "x"}).
		add(api.Merged, api.MergedPayload{TicketID: "root/c2", Commit: "y"}).
		fold()
	if !s.DepsSatisfied(s.Tickets["dep"]) {
		t.Error("dep should be satisfied once all children of the decomposed parent merge")
	}
}

func TestBlockedByFailedDependency(t *testing.T) {
	s := newBuild(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "a", Title: "a", IdempotencyKey: "ka"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "b", Title: "b", DependsOn: []string{"a"}, IdempotencyKey: "kb"}).
		add(api.TicketFailed, api.TicketFailedPayload{TicketID: "a", Reason: "boom"}).
		fold()

	blocked := s.Blocked()
	if len(blocked) != 1 || blocked[0].ID != "b" {
		t.Fatalf("Blocked = %v, want [b]", ids(blocked))
	}
	if got := s.DeadDependency(s.Tickets["b"]); got != "a" {
		t.Errorf("DeadDependency(b) = %q, want a", got)
	}
}

func TestDeadDependencyThroughDecomposition(t *testing.T) {
	s := newBuild(t).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "p", Title: "p", IdempotencyKey: "kp"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "p/c", Title: "c", IdempotencyKey: "kc", Depth: 1}).
		add(api.TicketDecomposed, api.TicketDecomposedPayload{TicketID: "p", Children: []string{"p/c"}}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "d", Title: "d", DependsOn: []string{"p"}, IdempotencyKey: "kd"}).
		add(api.TicketFailed, api.TicketFailedPayload{TicketID: "p/c", Reason: "boom"}).
		fold()

	if got := s.DeadDependency(s.Tickets["d"]); got != "p" {
		t.Errorf("d should be dead via p's failed child; DeadDependency = %q, want p", got)
	}
}

func ids(ts []*Ticket) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

func TestGoalTokensSpentAccumulates(t *testing.T) {
	s := newBuild(t).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "build"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "root", GoalID: "g1", Title: "root", IdempotencyKey: "g1:root"}).
		add(api.TicketDecomposed, api.TicketDecomposedPayload{TicketID: "root", Children: []string{"c"}, Tokens: 250}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "c", GoalID: "g1", Title: "child", IdempotencyKey: "g1:c"}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "c"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "c", Worker: "w"}).
		add(api.WorkStarted, api.WorkStartedPayload{TicketID: "c", Worker: "w"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "c", Commit: "x", Tokens: 750}).
		fold()

	g := s.Goals["g1"]
	if g == nil {
		t.Fatal("goal g1 missing")
	}
	if g.TokensSpent != 1000 {
		t.Errorf("TokensSpent = %d, want 1000 (250 decompose + 750 proposal)", g.TokensSpent)
	}
	if g.BudgetExceeded {
		t.Error("BudgetExceeded should be false without a GoalBudgetExceeded event")
	}
}

func TestGoalBudgetExceededSetsFlag(t *testing.T) {
	s := newBuild(t).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "build"}).
		add(api.GoalBudgetExceeded, api.GoalBudgetExceededPayload{GoalID: "g1", SpentTokens: 1000, Limit: 500}).
		fold()
	if !s.Goals["g1"].BudgetExceeded {
		t.Error("BudgetExceeded should be true after a GoalBudgetExceeded event")
	}
}
