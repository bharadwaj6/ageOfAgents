package orchestrator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/invariant"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
	"github.com/stretchr/testify/require"
)

// cancelGoal appends a GoalCancelled for goalID, as `aoa cancel` does.
func (h *harness) cancelGoal(t *testing.T, goalID string) {
	t.Helper()
	h.appendApproval(t, api.GoalCancelled, api.GoalCancelledPayload{GoalID: goalID, Reason: "issue closed"})
}

// propose puts a ticket of goalID on the log with a proposal in the merge queue.
// The branch does not exist: a proposal that is merged anyway fails loudly.
func (h *harness) propose(t *testing.T, goalID, ticketID string) {
	t.Helper()
	worker := "w-" + ticketID
	for _, ev := range []struct {
		typ     api.EventType
		payload any
	}{
		{api.TicketCreated, api.TicketCreatedPayload{TicketID: ticketID, GoalID: goalID, Title: ticketID, IdempotencyKey: ticketID}},
		{api.TicketReady, api.TicketReadyPayload{TicketID: ticketID}},
		{api.TicketClaimed, api.TicketClaimedPayload{TicketID: ticketID, Worker: worker}},
		{api.WorkStarted, api.WorkStartedPayload{TicketID: ticketID, Worker: worker}},
		{api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: ticketID, Worker: worker, Branch: "aoa/" + ticketID}},
	} {
		h.appendApproval(t, ev.typ, ev.payload)
	}
}

// requireCancelled asserts every named ticket failed because its Goal was
// cancelled, and that nothing on the log merged.
func requireCancelled(t *testing.T, h *harness, tickets ...string) {
	t.Helper()
	s := h.state(t)
	for _, id := range tickets {
		tk := s.Tickets[id]
		require.NotNil(t, tk, "ticket %s", id)
		require.Equal(t, state.StatusFailed, tk.Status, "ticket %s", id)
		require.Equal(t, "goal cancelled", tk.LastFailReason, "ticket %s", id)
	}
	events, err := h.led.Read()
	require.NoError(t, err)
	for _, e := range events {
		require.NotEqual(t, api.Merged, e.Type, "a cancelled goal's work merged: seq %d", e.Seq)
	}
	require.Empty(t, invariant.Check(events))
}

func TestCancelFailsQueuedAndParkedWork(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, h := setup(t, agent.NewMock(), pass, Options{Concurrency: 2, RequireApproval: true})
	h.submitGoal(t, "g1", "two tasks")
	// g1-ready waits for a worker; g1-parked passed the Gate and waits for a human.
	h.appendApproval(t, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g1-ready", GoalID: "g1", Title: "ready", IdempotencyKey: "g1:ready"})
	h.appendApproval(t, api.TicketReady, api.TicketReadyPayload{TicketID: "g1-ready"})
	h.propose(t, "g1", "g1-parked")
	h.appendApproval(t, api.VerificationPassed, api.VerificationPassedPayload{TicketID: "g1-parked", Worker: "w-g1-parked"})
	h.appendApproval(t, api.ApprovalRequested, api.ApprovalRequestedPayload{TicketID: "g1-parked", Worker: "w-g1-parked"})
	require.Equal(t, state.StatusAwaiting, h.state(t).Tickets["g1-parked"].Status)

	h.cancelGoal(t, "g1")
	require.NoError(t, o.ReconcileOnce(context.Background()))
	o.workers.Wait()

	requireCancelled(t, h, "g1-ready", "g1-parked")
	require.Zero(t, h.state(t).Tickets["g1-ready"].Attempts, "a cancelled goal's ticket was dispatched")
}

func TestCancelledGoalWithoutTicketsSettles(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, h := setup(t, agent.NewMock(), pass, Options{Concurrency: 2})
	h.submitGoal(t, "g1", "never mind")
	h.submitGoal(t, "g2", "add greeting")
	h.cancelGoal(t, "g1")

	require.NoError(t, o.Run(context.Background()))

	s := h.state(t)
	for _, tk := range s.Tickets {
		require.NotEqual(t, "g1", tk.GoalID, "a ticket was created for a cancelled goal: %s", tk.ID)
	}
	require.Equal(t, api.OutcomeCancelled, s.GoalOutcome("g1"))
	require.Equal(t, api.OutcomeMerged, s.GoalOutcome("g2"), "cancelling g1 must not stop g2")
	events, err := h.led.Read()
	require.NoError(t, err)
	require.Empty(t, invariant.Check(events))
	require.Empty(t, invariant.Settled(events))
}

// cancellingBackend cancels its own Goal mid-attempt, through a ledger handle of
// its own as a separate `aoa cancel` process would, then returns a real change.
type cancellingBackend struct {
	inner   agent.Backend
	logPath string
	goalID  string
}

func (*cancellingBackend) Name() string { return "cancelling" }

func (b *cancellingBackend) Run(ctx context.Context, task agent.Task) (agent.Result, error) {
	led, err := ledger.Open(b.logPath)
	if err != nil {
		return agent.Result{}, err
	}
	ev, err := api.NewEvent(api.GoalCancelled, "human", api.GoalCancelledPayload{GoalID: b.goalID})
	if err != nil {
		return agent.Result{}, err
	}
	if _, err := led.Append(ev); err != nil {
		return agent.Result{}, err
	}
	return b.inner.Run(ctx, task)
}

func TestCancelDuringAttemptPreventsMerge(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	backend := &cancellingBackend{inner: agent.NewMock(), goalID: "g1"}
	o, h := setup(t, backend, pass, Options{Concurrency: 1})
	backend.logPath = filepath.Join(h.base, "events.jsonl")
	h.submitGoal(t, "g1", "add greeting")

	require.NoError(t, o.Run(context.Background()))

	requireCancelled(t, h, "g1-impl")
	if _, err := os.Stat(filepath.Join(h.repo.Dir, "g1-impl.txt")); !os.IsNotExist(err) {
		t.Error("a cancelled goal's change reached main")
	}
	events, err := h.led.Read()
	require.NoError(t, err)
	require.Empty(t, invariant.Settled(events))
}

// A cancel that lands after a pass has read the log — while an earlier
// proposal's Gate runs, say — must still stop the merge queue, on the batched
// path and the one-at-a-time path alike, and must stop dispatch in that pass.
func TestCancelLandingMidPassIsHonouredBeforeMerge(t *testing.T) {
	for _, proposals := range []int{1, 2} { // two engage the batched merge path
		t.Run(fmt.Sprintf("%d proposals", proposals), func(t *testing.T) {
			pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
			o, h := setup(t, agent.NewMock(), pass, Options{Concurrency: 2})
			h.submitGoal(t, "g1", "work")
			var tickets []string
			for i := range proposals {
				id := fmt.Sprintf("g1-p%d", i)
				h.propose(t, "g1", id)
				tickets = append(tickets, id)
			}
			h.appendApproval(t, api.TicketCreated, api.TicketCreatedPayload{TicketID: "g1-late", GoalID: "g1", Title: "late", IdempotencyKey: "g1:late"})

			// Cancel the moment the pass promotes g1-late: after the pass read the
			// log for cancellations, before it dispatches or merges.
			canceller, err := ledger.Open(filepath.Join(h.base, "events.jsonl"))
			require.NoError(t, err)
			var cancelErr error
			h.led.SetAppendHook(func(e api.Event) {
				if e.Type == api.TicketReady && e.TicketID() == "g1-late" {
					ev, err := api.NewEvent(api.GoalCancelled, "human", api.GoalCancelledPayload{GoalID: "g1"})
					if err == nil {
						_, err = canceller.Append(ev)
					}
					cancelErr = err
				}
			})
			require.NoError(t, o.ReconcileOnce(context.Background()))
			o.workers.Wait()
			h.led.SetAppendHook(nil)
			require.NoError(t, cancelErr)
			require.True(t, h.state(t).Goals["g1"].Cancelled, "the hook never cancelled g1")

			requireCancelled(t, h, tickets...)
			require.Zero(t, h.state(t).Tickets["g1-late"].Attempts, "a cancelled goal's ticket was dispatched")
		})
	}
}
