package state

import (
	"encoding/json"
	"strings"
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
		add(api.VerificationFailed, api.VerificationFailedPayload{TicketID: "t1", Worker: "w1", Reason: "tests failed", Output: "FAIL: TestThing\n--- expected 1 got 2"}).
		fold()

	tk := s.Tickets["t1"]
	if tk.Status != StatusReady {
		t.Errorf("status = %s, want ready (re-dispatchable)", tk.Status)
	}
	if tk.Worker != "" || tk.Commit != "" {
		t.Errorf("proposal fields should be cleared, got worker=%q commit=%q", tk.Worker, tk.Commit)
	}
	if tk.LastFailOutput != "FAIL: TestThing\n--- expected 1 got 2" {
		t.Errorf("LastFailOutput = %q, want the verifier output projected for the retry prompt", tk.LastFailOutput)
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
	if got := len(s.ReadyTickets()); got != 2 {
		t.Errorf("ReadyTickets = %d, want 2 (t1 is claimed but can accept parallel workers, t2 is ready)", got)
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

func TestFailedAndRestartedAttemptsChargeTokens(t *testing.T) {
	// Attempts that never reach a proposal still burn budget: a retried attempt
	// (WorkerRestarted) and a terminal failure (TicketFailed) must both charge
	// the Goal, or the spend governor is blind to the failure spiral.
	s := newBuild(t).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "build"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "t1", IdempotencyKey: "g1:t1"}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "t1"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "t1", Worker: "w1"}).
		add(api.WorkerRestarted, api.WorkerRestartedPayload{TicketID: "t1", Worker: "w1", Tokens: 300, Model: "m"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "t1", Worker: "w2"}).
		add(api.TicketFailed, api.TicketFailedPayload{TicketID: "t1", Worker: "w2", Reason: "no changes", Tokens: 200, Model: "m"}).
		fold()

	g := s.Goals["g1"]
	if g.TokensSpent != 500 {
		t.Errorf("TokensSpent = %d, want 500 (300 restarted + 200 failed)", g.TokensSpent)
	}
	if g.TokensByModel["m"] != 500 {
		t.Errorf("TokensByModel[m] = %d, want 500", g.TokensByModel["m"])
	}
	if s.Tickets["t1"].Status != StatusFailed {
		t.Errorf("t1 status = %s, want failed", s.Tickets["t1"].Status)
	}
}

func TestStallRestartChargesNothing(t *testing.T) {
	// The Stall Detector restarts a worker without an attempt result, so it
	// reports no usage and must not invent spend.
	s := newBuild(t).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "build"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "t1", IdempotencyKey: "g1:t1"}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: "t1", Worker: "w1"}).
		add(api.WorkerStalled, api.WorkerStalledPayload{TicketID: "t1", Worker: "w1"}).
		add(api.WorkerRestarted, api.WorkerRestartedPayload{TicketID: "t1", Worker: "w1"}).
		fold()

	if got := s.Goals["g1"].TokensSpent; got != 0 {
		t.Errorf("TokensSpent = %d, want 0 (a stall restart reports no usage)", got)
	}
}

func TestDuplicateGoalKeyIsIgnored(t *testing.T) {
	// An at-least-once source (a redelivered webhook) may put the same logical
	// Goal on the log more than once. Replay must collapse them, and must not
	// reset the spend already recorded against the first one.
	s := newBuild(t).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "fix it", IdempotencyKey: "d-1"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "t1", IdempotencyKey: "g1:t1"}).
		add(api.TicketFailed, api.TicketFailedPayload{TicketID: "t1", Reason: "x", Tokens: 400, Model: "m"}).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g2", Text: "fix it", IdempotencyKey: "d-1"}).
		fold()

	if len(s.Goals) != 1 {
		t.Fatalf("goal count = %d, want 1 (same idempotency key)", len(s.Goals))
	}
	if s.Goals["g1"] == nil {
		t.Fatal("the first goal should be the one that survives")
	}
	if got := s.Goals["g1"].TokensSpent; got != 400 {
		t.Errorf("TokensSpent = %d, want 400 (a redelivery must not reset spend)", got)
	}
}

func TestUnkeyedGoalsAreIndependent(t *testing.T) {
	// Without a key there is nothing to dedupe on: two CLI submissions of the
	// same text are two distinct Goals, as they always have been.
	s := newBuild(t).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "same words"}).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g2", Text: "same words"}).
		fold()
	if len(s.Goals) != 2 {
		t.Errorf("goal count = %d, want 2 (unkeyed goals are independent)", len(s.Goals))
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

func TestGoalAmendmentAppendsToEffectiveText(t *testing.T) {
	s := newBuild(t).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "build a parser"}).
		add(api.GoalAmended, api.GoalAmendedPayload{GoalID: "g1", Guidance: "also handle comments"}).
		add(api.GoalAmended, api.GoalAmendedPayload{GoalID: "g1", Guidance: "use the stdlib scanner"}).
		fold()

	g := s.Goals["g1"]
	if g == nil {
		t.Fatal("goal g1 missing")
	}
	if len(g.Amendments) != 2 {
		t.Fatalf("Amendments = %v, want 2", g.Amendments)
	}
	eff := g.EffectiveText()
	for _, want := range []string{"build a parser", "also handle comments", "use the stdlib scanner"} {
		if !strings.Contains(eff, want) {
			t.Errorf("EffectiveText() = %q, missing %q", eff, want)
		}
	}
	// An un-amended goal returns its text verbatim.
	if (&Goal{Text: "plain"}).EffectiveText() != "plain" {
		t.Error("un-amended goal should return its text unchanged")
	}
}

func TestGoalFoldKeepsOrigin(t *testing.T) {
	// A front door that submitted a Goal needs to find it again — which issue it
	// came from, who asked — and a duplicate submit must name the original.
	origin := api.GoalSubmittedPayload{
		GoalID: "g1", Text: "fix it", Source: "linear", IdempotencyKey: "linear:ENG-1",
		Ref: "https://linear.app/acme/issue/ENG-1", By: "octocat",
	}
	tests := []struct {
		name  string
		build func(b *build) *build
		// wantIdx is the index of the event whose envelope the Goal must carry.
		wantIdx int
		want    api.GoalSubmittedPayload
	}{
		{
			name:    "origin fields are kept",
			build:   func(b *build) *build { return b.add(api.GoalSubmitted, origin) },
			wantIdx: 0,
			want:    origin,
		},
		{
			name: "a keyed duplicate does not overwrite the original",
			build: func(b *build) *build {
				dup := origin
				dup.GoalID, dup.Ref, dup.By, dup.Source = "g2", "https://elsewhere", "mallory", "poller"
				return b.add(api.GoalSubmitted, origin).add(api.GoalSubmitted, dup)
			},
			wantIdx: 0,
			want:    origin,
		},
		{
			name: "an unkeyed goal without origin has empty fields",
			build: func(b *build) *build {
				return b.add(api.Heartbeat, api.HeartbeatPayload{Worker: "w"}).
					add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "plain"})
			},
			wantIdx: 1,
			want:    api.GoalSubmittedPayload{GoalID: "g1", Text: "plain"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := tt.build(newBuild(t))
			s := b.fold()
			if len(s.Goals) != 1 {
				t.Fatalf("goal count = %d, want 1", len(s.Goals))
			}
			g := s.Goals[tt.want.GoalID]
			if g == nil {
				t.Fatalf("goal %s missing", tt.want.GoalID)
			}
			ev := b.events[tt.wantIdx]
			if g.Source != tt.want.Source || g.Ref != tt.want.Ref || g.By != tt.want.By {
				t.Errorf("origin = (source %q, ref %q, by %q), want (%q, %q, %q)",
					g.Source, g.Ref, g.By, tt.want.Source, tt.want.Ref, tt.want.By)
			}
			if !g.SubmittedAt.Equal(ev.Timestamp) {
				t.Errorf("SubmittedAt = %v, want the event's timestamp %v", g.SubmittedAt, ev.Timestamp)
			}
			if g.SubmittedSeq != ev.Seq {
				t.Errorf("SubmittedSeq = %d, want %d", g.SubmittedSeq, ev.Seq)
			}
		})
	}
}

func TestSnapshotKeepsGoalOrigin(t *testing.T) {
	// The origin fields are additive: a snapshot taken now carries them, and a
	// snapshot taken before they existed still decodes, with them empty.
	b := newBuild(t).add(api.GoalSubmitted, api.GoalSubmittedPayload{
		GoalID: "g1", Text: "fix it", Source: "linear", Ref: "linear:ENG-1", By: "octocat",
	})
	orig := b.fold()
	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	tests := []struct {
		name  string
		state json.RawMessage
		want  Goal
	}{
		{
			name:  "current snapshot",
			state: raw,
			want:  *orig.Goals["g1"],
		},
		{
			name:  "snapshot from before origin fields",
			state: json.RawMessage(`{"Goals":{"g1":{"ID":"g1","Text":"fix it","TokensByModel":{}}},"Tickets":{}}`),
			want:  Goal{ID: "g1", Text: "fix it", TokensByModel: map[string]int{}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newBuild(t).add(api.StateSnapshot, api.StateSnapshotPayload{State: tt.state}).fold()
			g := s.Goals["g1"]
			if g == nil {
				t.Fatal("goal g1 missing after snapshot")
			}
			if g.Source != tt.want.Source || g.Ref != tt.want.Ref || g.By != tt.want.By ||
				!g.SubmittedAt.Equal(tt.want.SubmittedAt) || g.SubmittedSeq != tt.want.SubmittedSeq {
				t.Errorf("goal after snapshot = %+v, want %+v", *g, tt.want)
			}
		})
	}
}

func TestApprovalDecisionIsRecorded(t *testing.T) {
	// `aoa approve` must tell a retried decision from a contradicting one, so
	// replay records which decision took effect, and where.
	parked := func(b *build) *build {
		return b.
			add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "impl"}).
			add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "t1", Worker: "w1", Branch: "aoa/t1"}).
			add(api.ApprovalRequested, api.ApprovalRequestedPayload{TicketID: "t1", Worker: "w1"})
	}
	tests := []struct {
		name         string
		decide       func(b *build) *build
		wantApproved bool
		wantRejected bool
		wantSeq      int
		wantStatus   TicketStatus
	}{
		{
			name:       "undecided",
			decide:     func(b *build) *build { return b },
			wantSeq:    0,
			wantStatus: StatusAwaiting,
		},
		{
			name: "approved",
			decide: func(b *build) *build {
				return b.add(api.ApprovalGranted, api.ApprovalGrantedPayload{TicketID: "t1", By: "octocat"})
			},
			wantApproved: true, wantSeq: 4, wantStatus: StatusProposed,
		},
		{
			name: "rejected",
			decide: func(b *build) *build {
				return b.add(api.ApprovalDenied, api.ApprovalDeniedPayload{TicketID: "t1", Reason: "no"})
			},
			wantRejected: true, wantSeq: 4, wantStatus: StatusFailed,
		},
		{
			name: "a late contradicting decision does not take effect",
			decide: func(b *build) *build {
				return b.add(api.ApprovalDenied, api.ApprovalDeniedPayload{TicketID: "t1"}).
					add(api.ApprovalGranted, api.ApprovalGrantedPayload{TicketID: "t1"})
			},
			wantRejected: true, wantSeq: 4, wantStatus: StatusFailed,
		},
		{
			name: "a repeated decision keeps the first seq",
			decide: func(b *build) *build {
				return b.add(api.ApprovalGranted, api.ApprovalGrantedPayload{TicketID: "t1"}).
					add(api.ApprovalGranted, api.ApprovalGrantedPayload{TicketID: "t1"})
			},
			wantApproved: true, wantSeq: 4, wantStatus: StatusProposed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tk := tt.decide(parked(newBuild(t))).fold().Tickets["t1"]
			if tk == nil {
				t.Fatal("ticket t1 missing")
			}
			if tk.Approved != tt.wantApproved || tk.Rejected != tt.wantRejected {
				t.Errorf("approved, rejected = %v, %v; want %v, %v", tk.Approved, tk.Rejected, tt.wantApproved, tt.wantRejected)
			}
			if tk.DecidedSeq != tt.wantSeq {
				t.Errorf("DecidedSeq = %d, want %d", tk.DecidedSeq, tt.wantSeq)
			}
			if tk.Status != tt.wantStatus {
				t.Errorf("status = %s, want %s", tk.Status, tt.wantStatus)
			}
		})
	}
}

// Ticket lifecycle steps for TestGoalOutcome, each appended to b and returned.
func created(b *build, id, goal string) *build {
	return b.add(api.TicketCreated, api.TicketCreatedPayload{TicketID: id, GoalID: goal, Title: id, IdempotencyKey: id})
}

func running(b *build, id string) *build {
	return b.add(api.TicketReady, api.TicketReadyPayload{TicketID: id}).
		add(api.TicketClaimed, api.TicketClaimedPayload{TicketID: id, Worker: "w-" + id}).
		add(api.WorkStarted, api.WorkStartedPayload{TicketID: id, Worker: "w-" + id})
}

func proposed(b *build, id string) *build {
	return running(b, id).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: id, Worker: "w-" + id, Commit: "cand-" + id})
}

func awaiting(b *build, id string) *build {
	return proposed(b, id).
		add(api.ApprovalRequested, api.ApprovalRequestedPayload{TicketID: id, Worker: "w-" + id})
}

func merged(b *build, id string) *build {
	return proposed(b, id).
		add(api.Merged, api.MergedPayload{TicketID: id, Worker: "w-" + id, Commit: "merged-" + id})
}

func failed(b *build, id string) *build {
	return running(b, id).
		add(api.TicketFailed, api.TicketFailedPayload{TicketID: id, Worker: "w-" + id, Reason: "gate failed"})
}

// decomposed runs parent and splits it into children, creating each child
// under goal first, as the Scheduler does.
func decomposed(b *build, parent, goal string, children ...string) *build {
	running(b, parent)
	for _, c := range children {
		created(b, c, goal)
	}
	return b.add(api.TicketDecomposed, api.TicketDecomposedPayload{TicketID: parent, Children: children})
}

func TestGoalOutcome(t *testing.T) {
	tests := []struct {
		name  string
		steps func(b *build) *build // run after g1 (and g2) are submitted
		goal  string
		want  string
	}{
		{name: "no ticket yet", steps: func(b *build) *build { return b }, want: api.OutcomeQueued},
		{name: "unknown goal", steps: func(b *build) *build { return b }, goal: "nope", want: ""},
		{name: "ticket created, not yet ready", steps: func(b *build) *build { return created(b, "t1", "g1") }, want: api.OutcomeRunning},
		{name: "ticket running", steps: func(b *build) *build { return running(created(b, "t1", "g1"), "t1") }, want: api.OutcomeRunning},
		{name: "proposal in the merge queue", steps: func(b *build) *build { return proposed(created(b, "t1", "g1"), "t1") }, want: api.OutcomeRunning},
		{name: "parked for approval", steps: func(b *build) *build { return awaiting(created(b, "t1", "g1"), "t1") }, want: api.OutcomeAwaitingApproval},
		{name: "merged", steps: func(b *build) *build { return merged(created(b, "t1", "g1"), "t1") }, want: api.OutcomeMerged},
		{name: "failed", steps: func(b *build) *build { return failed(created(b, "t1", "g1"), "t1") }, want: api.OutcomeFailed},
		{
			name: "only ticket rejected by a human",
			steps: func(b *build) *build {
				return awaiting(created(b, "t1", "g1"), "t1").
					add(api.ApprovalDenied, api.ApprovalDeniedPayload{TicketID: "t1", By: "octocat"})
			},
			want: api.OutcomeFailed,
		},
		{
			name: "approved, back in the merge queue",
			steps: func(b *build) *build {
				return awaiting(created(b, "t1", "g1"), "t1").
					add(api.ApprovalGranted, api.ApprovalGrantedPayload{TicketID: "t1"})
			},
			want: api.OutcomeRunning,
		},
		{
			name: "decomposed, every child merged",
			steps: func(b *build) *build {
				b = decomposed(created(b, "t1", "g1"), "t1", "g1", "t1/a", "t1/b")
				return merged(merged(b, "t1/a"), "t1/b")
			},
			want: api.OutcomeMerged,
		},
		{
			name: "decomposed, partial success is failed",
			steps: func(b *build) *build {
				b = decomposed(created(b, "t1", "g1"), "t1", "g1", "t1/a", "t1/b")
				return failed(merged(b, "t1/a"), "t1/b")
			},
			want: api.OutcomeFailed,
		},
		{
			name: "decomposed, one child merged and one still running",
			steps: func(b *build) *build {
				b = decomposed(created(b, "t1", "g1"), "t1", "g1", "t1/a", "t1/b")
				return running(merged(b, "t1/a"), "t1/b")
			},
			want: api.OutcomeRunning,
		},
		{
			name: "awaiting approval outranks a failed sibling",
			steps: func(b *build) *build {
				b = decomposed(created(b, "t1", "g1"), "t1", "g1", "t1/a", "t1/b")
				return awaiting(failed(b, "t1/a"), "t1/b")
			},
			want: api.OutcomeAwaitingApproval,
		},
		{
			name: "decomposition names a child not on the log",
			steps: func(b *build) *build {
				running(created(b, "t1", "g1"), "t1")
				return b.add(api.TicketDecomposed, api.TicketDecomposedPayload{TicketID: "t1", Children: []string{"t1/ghost"}})
			},
			want: api.OutcomeRunning,
		},
		{
			name: "budget tripped, work still in flight",
			steps: func(b *build) *build {
				return running(created(b, "t1", "g1"), "t1").
					add(api.GoalBudgetExceeded, api.GoalBudgetExceededPayload{GoalID: "g1", SpentTokens: 10, Limit: 5})
			},
			want: api.OutcomeRunning,
		},
		{
			name: "budget tripped, settled",
			steps: func(b *build) *build {
				return merged(created(b, "t1", "g1"), "t1").
					add(api.GoalBudgetExceeded, api.GoalBudgetExceededPayload{GoalID: "g1", SpentTokens: 10, Limit: 5})
			},
			want: api.OutcomeFailed,
		},
		{
			name: "another goal's failure does not leak",
			steps: func(b *build) *build {
				return failed(created(merged(created(b, "t1", "g1"), "t1"), "t2", "g2"), "t2")
			},
			want: api.OutcomeMerged,
		},
		{
			name:  "cancelled before any ticket",
			steps: func(b *build) *build { return b.add(api.GoalCancelled, api.GoalCancelledPayload{GoalID: "g1"}) },
			want:  api.OutcomeCancelled,
		},
		{
			name: "cancelled while an attempt is still in flight",
			steps: func(b *build) *build {
				return running(created(b, "t1", "g1"), "t1").add(api.GoalCancelled, api.GoalCancelledPayload{GoalID: "g1"})
			},
			want: api.OutcomeCancelled,
		},
		{
			name: "another goal's cancel does not leak",
			steps: func(b *build) *build {
				return merged(created(b, "t1", "g1"), "t1").add(api.GoalCancelled, api.GoalCancelledPayload{GoalID: "g2"})
			},
			want: api.OutcomeMerged,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBuild(t).
				add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "one"}).
				add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g2", Text: "two"})
			goal := tt.goal
			if goal == "" {
				goal = "g1"
			}
			if got := tt.steps(b).fold().GoalOutcome(goal); got != tt.want {
				t.Errorf("GoalOutcome(%s) = %q, want %q", goal, got, tt.want)
			}
		})
	}
}

func TestGoalCancelledIsRecorded(t *testing.T) {
	s := newBuild(t).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "build"}).
		add(api.GoalCancelled, api.GoalCancelledPayload{GoalID: "g1", By: "octocat", Reason: "issue closed"}).
		add(api.GoalCancelled, api.GoalCancelledPayload{GoalID: "g1"}).
		add(api.GoalCancelled, api.GoalCancelledPayload{GoalID: "nope"}).
		fold()
	g := s.Goals["g1"]
	if !g.Cancelled {
		t.Fatal("Cancelled should be true after a GoalCancelled event")
	}
	if g.CancelledSeq != 2 {
		t.Errorf("CancelledSeq = %d, want 2 (the first cancel)", g.CancelledSeq)
	}
	if s.Goals["nope"] != nil {
		t.Error("cancelling an unknown goal must not create it")
	}
}

// A cancel fails a proposal parked for approval, which no other TicketFailed
// ever had to: the parked ticket must end failed, not stay awaiting.
func TestTicketFailedEndsAParkedProposal(t *testing.T) {
	b := newBuild(t).add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "one"})
	s := awaiting(created(b, "t1", "g1"), "t1").
		add(api.TicketFailed, api.TicketFailedPayload{TicketID: "t1", Worker: "w-t1", Reason: "goal cancelled"}).
		fold()
	if got := s.Tickets["t1"].Status; got != StatusFailed {
		t.Errorf("status = %s, want failed", got)
	}
}
