package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// runWait runs `aoa wait` with args against root and returns its stdout and
// error. A short --timeout keeps a wrong answer from hanging the suite.
func runWait(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() {
		err = cmdWait(append([]string{"--path", root, "--poll", "10ms"}, args...))
	})
	return out, err
}

// TestWaitExitsWithTheOutcome: `aoa wait` returns once the named Goals are
// Complete, exiting 0 when all of them landed and 1 when any failed or was
// cancelled. It waits for the Goals it names and nothing else — mixedLog also
// holds a Goal parked for approval and one still queued, so a wait that meant
// "the workspace settled" would time out (exit 4) on every row here.
func TestWaitExitsWithTheOutcome(t *testing.T) {
	tests := []struct {
		name  string
		log   func(*testing.T) *logBuilder
		goals []string
		code  int // 0 is a nil error
	}{
		{name: "merged", log: mixedLog, goals: []string{"g-merged"}},
		{name: "decomposed, every child merged", log: mixedLog, goals: []string{"g-split"}},
		{name: "two that landed", log: mixedLog, goals: []string{"g-merged", "g-split"}},
		{name: "failed", log: mixedLog, goals: []string{"g-failed"}, code: 1},
		{name: "one landed, one failed", log: mixedLog, goals: []string{"g-merged", "g-failed"}, code: 1},
		{name: "rejected by a human", log: rejectedLog, goals: []string{"g-1"}, code: 1},
		{name: "cancelled", log: cancelledLog(true), goals: []string{"g-1"}, code: 1},
		{name: "delivered", log: deliveryLog(true), goals: []string{"g-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := statusWorkspace(t, tt.log(t))
			_, err := runWait(t, root, append([]string{"--timeout", "2s"}, tt.goals...)...)
			if got := codeOf(err); got != tt.code {
				t.Fatalf("aoa wait %v exited %d (%v), want %d", tt.goals, got, err, tt.code)
			}
		})
	}
}

// codeOf is the exit status a command's error would give the process.
func codeOf(err error) int {
	if err == nil {
		return 0
	}
	return exitCode(err)
}

// TestWaitTimesOutWithItsOwnStatus: a Goal that is not Complete when --timeout
// expires exits 4 — neither 1, which would say it failed, nor 0 — and the error
// names what it was still waiting on. One incomplete Goal holds up the lot.
func TestWaitTimesOutWithItsOwnStatus(t *testing.T) {
	tests := []struct {
		name   string
		log    func(*testing.T) *logBuilder
		goals  []string
		reason string
	}{
		{name: "parked for approval", log: mixedLog, goals: []string{"g-await"}, reason: "AwaitingApproval"},
		{name: "queued", log: mixedLog, goals: []string{"g-queued"}, reason: "Queued"},
		{name: "one done, one parked", log: mixedLog, goals: []string{"g-merged", "g-await"}, reason: "AwaitingApproval"},
		{name: "merged onto its branch, not yet delivered", log: pendingDeliveryLog, goals: []string{"g-1"}, reason: "DeliveryPending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := statusWorkspace(t, tt.log(t))
			start := time.Now()
			_, err := runWait(t, root, append([]string{"--timeout", "100ms"}, tt.goals...)...)
			if got := codeOf(err); got != exitWaitTimeout {
				t.Fatalf("exited %d (%v), want %d", got, err, exitWaitTimeout)
			}
			if !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("error %q should say what it was waiting on (%s)", err, tt.reason)
			}
			if waited := time.Since(start); waited < 100*time.Millisecond {
				t.Errorf("gave up after %s, before the 100ms timeout", waited)
			}
		})
	}
}

// TestWaitReturnsWhenTheGoalCompletes is the point of the command: it is
// started before the work is done, and returns once the Event Log says so,
// without a second `aoa wait` or a caller-side poll loop.
func TestWaitReturnsWhenTheGoalCompletes(t *testing.T) {
	root, _ := statusWorkspace(t, runningLog(t))
	ws, err := openWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		time.Sleep(100 * time.Millisecond) // let wait see the Goal still running
		for _, e := range []struct {
			typ api.EventType
			p   any
		}{
			{api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "g-1-impl", Worker: "w1", Commit: "cand-1"}},
			{api.VerificationPassed, api.VerificationPassedPayload{TicketID: "g-1-impl", Worker: "w1"}},
			{api.Merged, api.MergedPayload{TicketID: "g-1-impl", Worker: "w1", Commit: "m1"}},
		} {
			ev, err := api.NewEvent(e.typ, "test", e.p)
			if err == nil {
				_, err = led.Append(ev)
			}
			if err != nil {
				finished <- err
				return
			}
		}
		finished <- nil
	}()

	start := time.Now()
	out, err := runWait(t, root, "--timeout", "10s", "--json", "g-1")
	if err != nil {
		t.Fatalf("aoa wait: %v", err)
	}
	if appendErr := <-finished; appendErr != nil {
		t.Fatalf("append: %v", appendErr)
	}
	if waited := time.Since(start); waited < 100*time.Millisecond {
		t.Errorf("returned after %s, before the Goal was merged", waited)
	}
	var gv api.GoalView
	decodeJSONLine(t, out, &gv)
	if gv.ID != "g-1" || gv.Outcome != api.OutcomeMerged || !goalDone(gv) {
		t.Errorf("printed %s outcome %q complete %v, want g-1 merged and complete", gv.ID, gv.Outcome, goalDone(gv))
	}
}

// TestWaitJSONPrintsOneGoalViewPerGoal: --json output is one GoalView per line,
// in the order the Goals were named, so a caller reads it with one decode each.
func TestWaitJSONPrintsOneGoalViewPerGoal(t *testing.T) {
	root, _ := statusWorkspace(t, mixedLog(t))
	out, err := runWait(t, root, "--timeout", "2s", "--json", "g-split", "g-merged")
	if err != nil {
		t.Fatalf("aoa wait: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want one per Goal: %q", len(lines), out)
	}
	for i, id := range []string{"g-split", "g-merged"} {
		var gv api.GoalView
		decodeJSONLine(t, lines[i]+"\n", &gv)
		if gv.ID != id {
			t.Errorf("line %d is %s, want %s", i+1, gv.ID, id)
		}
	}
}

// TestWaitRejectsAMissingOrUnknownGoal: no Goal to wait on, or one the Event Log
// does not hold, is a usage error (exit 2) reported at once — waiting on a typo
// would otherwise hang until --timeout, or forever.
func TestWaitRejectsAMissingOrUnknownGoal(t *testing.T) {
	root, _ := statusWorkspace(t, mixedLog(t))
	for name, args := range map[string][]string{
		"no goal":      nil,
		"unknown goal": {"g-nope"},
		"one of two":   {"g-merged", "g-nope"},
	} {
		t.Run(name, func(t *testing.T) {
			start := time.Now()
			_, err := runWait(t, root, args...) // no --timeout: it must not wait at all
			if got := codeOf(err); got != 2 {
				t.Fatalf("exited %d (%v), want 2", got, err)
			}
			var ee *exitError
			if !errors.As(err, &ee) {
				t.Errorf("error %v carries no exit status", err)
			}
			if waited := time.Since(start); waited > time.Second {
				t.Errorf("took %s to reject it", waited)
			}
		})
	}
}
