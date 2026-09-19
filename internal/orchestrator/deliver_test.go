package orchestrator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/invariant"
	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
	"github.com/stretchr/testify/require"
)

// prHarness is a harness in PR delivery mode: its repository is a clone of a
// bare origin, and the opener is a fake script that records each call.
type prHarness struct {
	*harness
	origin   string // the bare remote
	baseSHA  string // origin main when the test began
	opener   string // fake opener script, rewritten to switch behaviour
	calls    string // one line appended per opener call
	lastArgv string // the last call's argv, NUL-separated
}

// gitT runs git in dir with a fixed identity and returns its trimmed output.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...).CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// hasRef reports whether ref resolves in dir.
func hasRef(dir, ref string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", ref).Run() == nil
}

func setupPR(t *testing.T, backend agent.Backend, gate verify.Verifier, opt Options) (*Orchestrator, *prHarness) {
	t.Helper()
	dir := t.TempDir()
	p := &prHarness{
		opener:   filepath.Join(dir, "open-pr"),
		calls:    filepath.Join(dir, "calls"),
		lastArgv: filepath.Join(dir, "argv"),
	}
	p.setOpener(t, "echo https://example.test/pr/1")
	opt.Delivery = Delivery{Remote: "origin", Base: "main", OpenPR: []string{p.opener, "{branch}", "{base}", "{title}", "{body}"}}
	o, h := setup(t, backend, gate, opt)
	p.harness = h
	p.origin = filepath.Join(h.base, "origin.git")
	gitT(t, h.base, "init", "--bare", "--template=", "-b", "main", p.origin)
	gitT(t, h.repo.Dir, "remote", "add", "origin", p.origin)
	gitT(t, h.repo.Dir, "push", "origin", "main")
	p.baseSHA = gitT(t, p.origin, "rev-parse", "refs/heads/main")
	return o, p
}

// setOpener rewrites the fake opener: it records its call and argv, then runs
// then (which decides what it prints and how it exits).
func (p *prHarness) setOpener(t *testing.T, then string) {
	t.Helper()
	script := "#!/bin/sh\necho call >> '" + p.calls + "'\nprintf '%s\\0' \"$@\" > '" + p.lastArgv + "'\n" + then + "\n"
	require.NoError(t, os.WriteFile(p.opener, []byte(script), 0o755))
}

// openerCalls counts how many times the opener ran.
func (p *prHarness) openerCalls(t *testing.T) int {
	t.Helper()
	b, err := os.ReadFile(p.calls)
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return strings.Count(string(b), "call\n")
}

// events returns the payloads of every event of typ, decoded into T.
func eventsOf[T any](t *testing.T, h *harness, typ api.EventType) []T {
	t.Helper()
	events, err := h.led.Read()
	require.NoError(t, err)
	var out []T
	for _, e := range events {
		if e.Type == typ {
			var p T
			require.NoError(t, e.DecodePayload(&p))
			out = append(out, p)
		}
	}
	return out
}

func requireInvariants(t *testing.T, h *harness) {
	t.Helper()
	events, err := h.led.Read()
	require.NoError(t, err)
	require.Empty(t, invariant.Check(events))
	require.Empty(t, invariant.Settled(events))
}

// A decomposed Goal is delivered as one pull request from one branch: its
// children merge onto the Goal branch — the dependent one cut from it, with its
// sibling's work — which is pushed once complete. The remote base and the
// adopted repository's own branch are never written.
func TestPRModeDeliversADecomposedGoalAsOnePullRequest(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	mock := &agent.Mock{
		Decompose: map[string][]agent.Subtask{
			"Implement: build app": {
				{LocalID: "types", Title: "define shared User type", IdempotencyKey: "g1:types"},
				{LocalID: "api", Title: "implement the API", DependsOn: []string{"types"}, IdempotencyKey: "g1:api"},
			},
		},
	}
	o, p := setupPR(t, mock, pass, Options{Concurrency: 4})
	// Local work nobody pushed: the Goal branch is cut from the remote base,
	// so it must not reach the pull request.
	require.NoError(t, os.WriteFile(filepath.Join(p.repo.Dir, "local-only.txt"), []byte("x\n"), 0o644))
	gitT(t, p.repo.Dir, "add", "-A")
	gitT(t, p.repo.Dir, "commit", "-m", "local only")
	localMain := gitT(t, p.repo.Dir, "rev-parse", "refs/heads/main")

	p.appendApproval(t, api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "build app", Ref: "https://github.com/o/r/issues/3"})
	require.NoError(t, o.Run(context.Background()))

	require.Equal(t, p.baseSHA, gitT(t, p.origin, "rev-parse", "refs/heads/main"), "origin main must not move")
	require.Equal(t, localMain, gitT(t, p.repo.Dir, "rev-parse", "refs/heads/main"), "the adopted repo's branch must not move")
	files := gitT(t, p.origin, "ls-tree", "-r", "--name-only", "refs/heads/aoa/g1")
	require.Contains(t, files, "g1-impl/types.txt")
	require.Contains(t, files, "g1-impl/api.txt")
	require.NotContains(t, files, "local-only.txt")

	require.Equal(t, 1, p.openerCalls(t))
	argv, err := os.ReadFile(p.lastArgv)
	require.NoError(t, err)
	args := strings.Split(strings.TrimSuffix(string(argv), "\x00"), "\x00")
	require.Len(t, args, 4)
	require.Equal(t, []string{"aoa/g1", "main", "build app"}, args[:3])
	require.Contains(t, args[3], "Closes https://github.com/o/r/issues/3")
	require.Contains(t, args[3], "passed the Gate")

	delivered := eventsOf[api.DeliveredPayload](t, p.harness, api.Delivered)
	require.Equal(t, []api.DeliveredPayload{{
		GoalID: "g1", Branch: "aoa/g1", Commit: gitT(t, p.origin, "rev-parse", "refs/heads/aoa/g1"), URL: "https://example.test/pr/1",
	}}, delivered)
	for _, m := range eventsOf[api.MergedPayload](t, p.harness, api.Merged) {
		require.Equal(t, "aoa/g1", m.Branch, "ticket %s", m.TicketID)
	}
	require.Equal(t, api.OutcomeDelivered, p.state(t).GoalOutcome("g1"))
	requireInvariants(t, p.harness)
}

// A failed opener is recorded once per Run — not once per pass, although the
// Run keeps passing while another Goal works — and the next Run retries it.
func TestPRModeRetriesAFailedDeliveryOncePerRun(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, p := setupPR(t, agent.NewMock(), pass, Options{Concurrency: 1})
	p.setOpener(t, "echo 'gh: authentication required' >&2; exit 1")
	p.submitGoal(t, "g1", "first")
	p.submitGoal(t, "g2", "second")
	require.NoError(t, o.Run(context.Background()))

	failed := eventsOf[api.DeliveryFailedPayload](t, p.harness, api.DeliveryFailed)
	require.Len(t, failed, 2, "one failure per goal: %+v", failed)
	for _, f := range failed {
		require.Contains(t, f.Reason, "authentication required")
	}
	require.Empty(t, eventsOf[api.DeliveredPayload](t, p.harness, api.Delivered))
	g := p.state(t).Goals["g1"]
	require.Equal(t, api.OutcomeRunning, p.state(t).GoalOutcome("g1"), "pending delivery")
	require.Contains(t, g.DeliveryError, "authentication required")

	p.setOpener(t, "echo 'opening...'; echo https://example.test/pr/1")
	require.NoError(t, o.Run(context.Background()))
	require.Len(t, eventsOf[api.DeliveryFailedPayload](t, p.harness, api.DeliveryFailed), 2)
	delivered := eventsOf[api.DeliveredPayload](t, p.harness, api.Delivered)
	require.Len(t, delivered, 2)
	require.Equal(t, "https://example.test/pr/1", delivered[0].URL)
	s := p.state(t)
	require.Equal(t, api.OutcomeDelivered, s.GoalOutcome("g1"))
	require.Empty(t, s.Goals["g1"].DeliveryError)
	require.Equal(t, 4, p.openerCalls(t))
	requireInvariants(t, p.harness)
}

// A remote Goal branch holding a commit aoa did not make is refused, never
// overwritten, and nothing else on the remote moves.
func TestPRModeRefusesToOverwriteAForeignRemoteBranch(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, p := setupPR(t, agent.NewMock(), pass, Options{Concurrency: 1})
	gitT(t, p.repo.Dir, "commit", "--allow-empty", "-m", "someone else's work")
	foreign := gitT(t, p.repo.Dir, "rev-parse", "HEAD")
	gitT(t, p.repo.Dir, "push", "origin", "HEAD:refs/heads/aoa/g1")
	gitT(t, p.repo.Dir, "reset", "--hard", p.baseSHA)

	p.submitGoal(t, "g1", "build it")
	require.NoError(t, o.Run(context.Background()))

	failed := eventsOf[api.DeliveryFailedPayload](t, p.harness, api.DeliveryFailed)
	require.Len(t, failed, 1)
	require.Contains(t, failed[0].Reason, "rejected")
	require.Equal(t, foreign, gitT(t, p.origin, "rev-parse", "refs/heads/aoa/g1"), "the foreign commit must survive")
	require.Equal(t, p.baseSHA, gitT(t, p.origin, "rev-parse", "refs/heads/main"))
	require.Zero(t, p.openerCalls(t))
	requireInvariants(t, p.harness)
}

// Work the Gate rejects never moves the Goal branch, and a failed Goal is
// never pushed.
func TestPRModeNeverPushesWorkTheGateRejected(t *testing.T) {
	fail := verify.Verifier{Commands: []verify.Command{{"false"}}}
	o, p := setupPR(t, agent.NewMock(), fail, Options{Concurrency: 1, MaxAttempts: 2})
	p.submitGoal(t, "g1", "build it")
	require.NoError(t, o.Run(context.Background()))

	require.Equal(t, api.OutcomeFailed, p.state(t).GoalOutcome("g1"))
	require.Equal(t, p.baseSHA, gitT(t, p.repo.Dir, "rev-parse", "refs/heads/aoa/g1"), "the Goal branch stays at the base")
	require.False(t, hasRef(p.origin, "refs/heads/aoa/g1"), "nothing pushed")
	require.Empty(t, eventsOf[api.DeliveredPayload](t, p.harness, api.Delivered))
	require.Empty(t, eventsOf[api.DeliveryFailedPayload](t, p.harness, api.DeliveryFailed))
	require.Zero(t, p.openerCalls(t))
	requireInvariants(t, p.harness)
}

// A Goal cancelled while its delivery is pending is never pushed: the remote
// refuses the first push, the Goal is cancelled, and the next Run leaves it be.
func TestPRModeNeverPushesACancelledGoal(t *testing.T) {
	pass := verify.Verifier{Commands: []verify.Command{{"true"}}}
	o, p := setupPR(t, agent.NewMock(), pass, Options{Concurrency: 1})
	hook := filepath.Join(p.origin, "hooks", "pre-receive")
	require.NoError(t, os.MkdirAll(filepath.Dir(hook), 0o755))
	require.NoError(t, os.WriteFile(hook, []byte("#!/bin/sh\necho 'remote is read-only' >&2\nexit 1\n"), 0o755))
	p.submitGoal(t, "g1", "build it")
	require.NoError(t, o.Run(context.Background()))
	require.Len(t, eventsOf[api.DeliveryFailedPayload](t, p.harness, api.DeliveryFailed), 1)

	p.cancelGoal(t, "g1")
	require.NoError(t, os.Remove(hook))
	require.NoError(t, o.Run(context.Background()))

	require.Equal(t, api.OutcomeCancelled, p.state(t).GoalOutcome("g1"))
	require.False(t, hasRef(p.origin, "refs/heads/aoa/g1"), "a cancelled goal is never pushed")
	require.Len(t, eventsOf[api.DeliveryFailedPayload](t, p.harness, api.DeliveryFailed), 1, "no retry after the cancel")
	require.Empty(t, eventsOf[api.DeliveredPayload](t, p.harness, api.Delivered))
	require.Zero(t, p.openerCalls(t))
	requireInvariants(t, p.harness)
}
