package orchestrator

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/diagnose"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
	"github.com/stretchr/testify/require"
)

// TestFlakyGateLuckyRetryIsDiagnosed reproduces issue #104 end to end: a Gate
// that fails its first run and passes afterwards, against a deterministic
// backend that produces the identical patch both times. The merge happens — the
// Gate is the contract and the Gate said yes — but the Event Log now carries
// enough to see that it said *both* things about the same content, and
// diagnose surfaces it as flaky_gate.
func TestFlakyGateLuckyRetryIsDiagnosed(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "gate-ran")
	script := "if [ -f " + sentinel + " ]; then exit 0; fi; touch " + sentinel + "; exit 1"
	gate := verify.Verifier{Commands: []verify.Command{{"sh", "-c", script}}}
	o, h := setup(t, agent.NewMock(), gate, Options{Concurrency: 1, MaxAttempts: 3})
	h.submitGoal(t, "g1", "build app")

	require.NoError(t, o.Run(context.Background()))
	tk := h.state(t).Tickets["g1-impl"]
	require.NotNil(t, tk)
	require.Equal(t, state.StatusMerged, tk.Status, "the lucky retry merges; that is the bug being detected")

	events, err := h.led.Read()
	require.NoError(t, err)

	var failTree, passTree string
	for _, e := range events {
		switch e.Type {
		case api.VerificationFailed:
			var p api.VerificationFailedPayload
			require.NoError(t, e.DecodePayload(&p))
			failTree = p.Tree
		case api.VerificationPassed:
			var p api.VerificationPassedPayload
			require.NoError(t, e.DecodePayload(&p))
			passTree = p.Tree
		}
	}
	require.NotEmpty(t, failTree, "the rejection must record the tree the Gate ran against")
	require.Equal(t, failTree, passTree, "the retry changed nothing, so both runs verified one tree")

	rep := diagnose.Classify(events)
	for _, f := range rep.Findings {
		if f.Mode == diagnose.FlakyGate {
			require.Equal(t, 1, f.Count)
			require.Equal(t, []string{"g1-impl"}, f.Tickets)
			return
		}
	}
	t.Fatal("flaky_gate missing from the diagnose report")
}

// TestDeterministicGateIsNotFlagged is the mutation check at the orchestrator
// level: the same retry path with a Gate that simply always passes must leave
// flaky_gate at 0.
func TestDeterministicGateIsNotFlagged(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	mock := agent.NewMock()
	mock.FailTitles = map[string]bool{} // the Gate never disagrees with itself
	o, h := setup(t, mock, pass, Options{Concurrency: 1, MaxAttempts: 3})
	h.submitGoal(t, "g1", "build app")
	require.NoError(t, o.Run(context.Background()))

	events, err := h.led.Read()
	require.NoError(t, err)
	for _, f := range diagnose.Classify(events).Findings {
		if f.Mode == diagnose.FlakyGate {
			require.Equal(t, 0, f.Count, "a deterministic Gate is not flaky: %+v", f)
			return
		}
	}
	t.Fatal("flaky_gate missing from the diagnose report")
}
