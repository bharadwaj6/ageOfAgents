package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/invariant"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

func (h *harness) appendApproval(t *testing.T, typ api.EventType, payload any) {
	t.Helper()
	ev, err := api.NewEvent(typ, "human", payload)
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	if _, err := h.led.Append(ev); err != nil {
		t.Fatalf("append: %v", err)
	}
}

func (h *harness) checkInvariants(t *testing.T) {
	t.Helper()
	events, _ := h.led.Read()
	if vs := invariant.Check(events); len(vs) != 0 {
		t.Fatalf("invariant violations: %v", vs)
	}
}

func TestApprovalGateParksThenMergesOnApprove(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, h := setup(t, agent.NewMock(), pass, Options{Concurrency: 2, RequireApproval: true})
	h.submitGoal(t, "g1", "add greeting")

	// First run parks the verified proposal for approval and returns cleanly.
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run (pre-approval): %v", err)
	}
	s := h.state(t)
	if tk := s.Tickets["g1-impl"]; tk == nil || tk.Status != state.StatusAwaiting {
		t.Fatalf("ticket = %+v, want awaiting approval", tk)
	}
	if _, err := os.Stat(filepath.Join(h.repo.Dir, "g1-impl.txt")); !os.IsNotExist(err) {
		t.Fatal("nothing must reach main before approval")
	}
	h.checkInvariants(t)

	// A human approves; the next run performs the real verify + merge.
	h.appendApproval(t, api.ApprovalGranted, api.ApprovalGrantedPayload{TicketID: "g1-impl"})
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run (post-approval): %v", err)
	}
	s = h.state(t)
	if tk := s.Tickets["g1-impl"]; tk == nil || tk.Status != state.StatusMerged {
		t.Fatalf("ticket = %+v, want merged", tk)
	}
	if !s.Settled() {
		t.Error("expected settled after approval+merge")
	}
	if _, err := os.Stat(filepath.Join(h.repo.Dir, "g1-impl.txt")); err != nil {
		t.Errorf("approved file should be on main: %v", err)
	}
	h.checkInvariants(t)
}

func TestApprovalGateFailsTicketOnReject(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, h := setup(t, agent.NewMock(), pass, Options{Concurrency: 2, RequireApproval: true})
	h.submitGoal(t, "g1", "add greeting")

	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run (pre-approval): %v", err)
	}
	if tk := h.state(t).Tickets["g1-impl"]; tk == nil || tk.Status != state.StatusAwaiting {
		t.Fatalf("ticket = %+v, want awaiting approval", tk)
	}

	// A human rejects; the ticket fails terminally and nothing reaches main.
	h.appendApproval(t, api.ApprovalDenied, api.ApprovalDeniedPayload{TicketID: "g1-impl", Reason: "no"})
	if err := o.Run(context.Background()); err != nil {
		t.Fatalf("Run (post-reject): %v", err)
	}
	s := h.state(t)
	if tk := s.Tickets["g1-impl"]; tk == nil || tk.Status != state.StatusFailed {
		t.Fatalf("ticket = %+v, want failed", tk)
	}
	if !s.Settled() {
		t.Error("expected settled after reject")
	}
	if _, err := os.Stat(filepath.Join(h.repo.Dir, "g1-impl.txt")); !os.IsNotExist(err) {
		t.Error("rejected work must not be on main")
	}
	h.checkInvariants(t)
}
