package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
	"github.com/stretchr/testify/require"
)

// sessionsRepo builds a repo with three agent sessions in linked worktrees:
// one that committed a change, one that edited a test without committing, and
// one that has done nothing. It returns the repo and the worktree paths.
func sessionsRepo(t *testing.T) (repo string, wt map[string]string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx := context.Background()
	base := t.TempDir()
	r, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)
	writeFile(t, filepath.Join(r.Dir, "go.mod"), "module demo\n\ngo 1.26\n")
	writeFile(t, filepath.Join(r.Dir, "auth.go"), "package demo\n\nfunc Login() bool { return true }\n")
	writeFile(t, filepath.Join(r.Dir, "auth_test.go"), "package demo\n\nimport \"testing\"\n\nfunc TestLogin(t *testing.T) {\n\tif !Login() {\n\t\tt.Fatal(\"login\")\n\t}\n}\n")
	_, _, err = r.CommitAll(ctx, "seed")
	require.NoError(t, err)

	wt = map[string]string{}
	for _, name := range []string{"login", "tests", "idle"} {
		w, err := r.AddWorktree(ctx, filepath.Join(base, "wt", name), "agent/"+name)
		require.NoError(t, err)
		wt[name] = w.Path
	}
	writeFile(t, filepath.Join(wt["login"], "auth.go"), "package demo\n\nfunc Login() bool { return true }\n\nfunc Logout() {}\n")
	writeFile(t, filepath.Join(wt["tests"], "auth_test.go"), "package demo\n\nfunc TestLogin() {}\n")
	return r.Dir, wt
}

func sessionsState(t *testing.T, repo string) *state.State {
	t.Helper()
	path, err := sessionLedgerPath(context.Background(), repo)
	require.NoError(t, err)
	led, err := ledger.Open(path)
	require.NoError(t, err)
	events, err := led.Read()
	require.NoError(t, err)
	st, err := state.Fold(events)
	require.NoError(t, err)
	return st
}

// The table lists every session, marks the one that touched a test, and the
// log lands inside the repository's git directory — no workspace, no config,
// and nothing written into any working tree.
func TestSessionsListsWorktreesAndFlagsTestEdits(t *testing.T) {
	repo, wt := sessionsRepo(t)

	var err error
	out := captureStdout(t, func() { err = cmdSessions([]string{"--repo", repo}) })
	require.NoError(t, err)

	for _, branch := range []string{"agent/login", "agent/tests", "agent/idle"} {
		require.Contains(t, out, branch)
	}
	require.Contains(t, out, "auth_test.go", "the test edit is named")
	require.Contains(t, out, "Test files touched")
	require.NotContains(t, out, "agent/login  auth_test.go", "only the session that touched a test is listed")

	require.FileExists(t, filepath.Join(repo, ".git", "aoa", "events.jsonl"))
	entries, err := os.ReadDir(repo)
	require.NoError(t, err)
	for _, e := range entries {
		require.NotEqual(t, "aoa.toml", e.Name(), "sessions needs no workspace")
	}

	st := sessionsState(t, repo)
	require.Len(t, st.Sessions, 3)
	byBranch := map[string]*state.Session{}
	for _, x := range st.Sessions {
		byBranch[x.Branch] = x
	}
	require.Equal(t, []string{"auth.go"}, byBranch["agent/login"].Files)
	require.Empty(t, byBranch["agent/login"].TestFiles)
	require.Equal(t, []string{"auth_test.go"}, byBranch["agent/tests"].TestFiles)
	require.True(t, byBranch["agent/tests"].Dirty)
	require.Empty(t, byBranch["agent/idle"].Files)
	require.Equal(t, idOf(t, wt["idle"]), byBranch["agent/idle"].ID)
}

// Observing twice records nothing the second time; observing after the tree
// changes records the change. Running it on a loop must not grow the log.
func TestSessionsRecordsOnlyWhatChanged(t *testing.T) {
	repo, wt := sessionsRepo(t)

	count := func() int {
		path, err := sessionLedgerPath(context.Background(), repo)
		require.NoError(t, err)
		led, err := ledger.Open(path)
		require.NoError(t, err)
		events, err := led.Read()
		require.NoError(t, err)
		return len(events)
	}
	run := func() {
		var err error
		captureStdout(t, func() { err = cmdSessions([]string{"--repo", repo}) })
		require.NoError(t, err)
	}

	run()
	first := count()
	require.Equal(t, 3, first)

	run()
	require.Equal(t, first, count(), "an unchanged repository appends nothing")

	writeFile(t, filepath.Join(wt["idle"], "notes.md"), "scratch\n")
	run()
	require.Equal(t, first+1, count(), "a changed tree is recorded once")
	run()
	require.Equal(t, first+1, count())
}

// When a worktree is removed the session stays on the log, marked gone, with
// what it last showed — the point of a ledger is the sessions you cleaned up.
func TestSessionsRecordsRemovedWorktrees(t *testing.T) {
	repo, wt := sessionsRepo(t)
	var err error
	captureStdout(t, func() { err = cmdSessions([]string{"--repo", repo}) })
	require.NoError(t, err)
	gone := idOf(t, wt["login"])

	ctx := context.Background()
	r := worktree.OpenRepo(repo)
	require.NoError(t, r.Remove(ctx, &worktree.Worktree{Path: wt["login"], Branch: "agent/login"}))

	out := captureStdout(t, func() { err = cmdSessions([]string{"--repo", repo, "--all"}) })
	require.NoError(t, err)
	require.Contains(t, out, "worktree gone")

	st := sessionsState(t, repo)
	x := st.Sessions[gone]
	require.NotNil(t, x)
	require.True(t, x.Removed)
	require.Equal(t, []string{"auth.go"}, x.Files, "what it changed is still on the log")
}

// The Gate runs on the named session's tree and its verdict is recorded
// against the content it saw; a later edit makes that verdict stale.
func TestSessionsCheckRecordsVerdictAndGoesStale(t *testing.T) {
	repo, wt := sessionsRepo(t)

	var err error
	out := captureStdout(t, func() {
		err = cmdSessionsCheck([]string{"--repo", repo, "--gate", "go vet ./...", "agent/login"})
	})
	require.NoError(t, err, out)
	require.Contains(t, out, "Gate passed")

	st := sessionsState(t, repo)
	x := st.Sessions[idOf(t, wt["login"])]
	require.NotNil(t, x.Check)
	require.True(t, x.Check.Passed)
	require.Equal(t, "go vet ./...", x.Check.Command)
	require.False(t, x.CheckStale())

	writeFile(t, filepath.Join(wt["login"], "auth.go"), "package demo\n\nfunc Login() bool { return false }\n")
	captureStdout(t, func() { err = cmdSessions([]string{"--repo", repo}) })
	require.NoError(t, err)

	st = sessionsState(t, repo)
	x = st.Sessions[idOf(t, wt["login"])]
	require.True(t, x.CheckStale(), "the tree changed after the check")
	require.Contains(t, captureStdout(t, func() { err = cmdSessions([]string{"--repo", repo}) }), "pass (stale)")
	require.NoError(t, err)
}

// A failing Gate fails the command, and the failure is on the log with the
// tail of its output.
func TestSessionsCheckFailingGate(t *testing.T) {
	repo, wt := sessionsRepo(t)
	writeFile(t, filepath.Join(wt["tests"], "broken.go"), "package demo\n\nfunc Broken() int { return \"not an int\" }\n")

	var err error
	out := captureStdout(t, func() {
		err = cmdSessionsCheck([]string{"--repo", repo, "--gate", "go build ./...", "agent/tests"})
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "gate failed")
	require.Contains(t, out, "broken.go")

	st := sessionsState(t, repo)
	x := st.Sessions[idOf(t, wt["tests"])]
	require.NotNil(t, x.Check)
	require.False(t, x.Check.Passed)
	require.Contains(t, x.Check.Output, "broken.go")
}

// A session is named by ID, by a prefix of it, by branch or by path; anything
// else says what the repository actually has.
func TestFindSessionResolvesEveryHandle(t *testing.T) {
	repo, wt := sessionsRepo(t)
	sc, err := scanSessions(context.Background(), repo, "")
	require.NoError(t, err)
	id := idOf(t, wt["tests"])

	for _, target := range []string{id, id[:6], "agent/tests", wt["tests"]} {
		i, err := findSession(sc, target)
		require.NoError(t, err, target)
		require.Equal(t, id, sc.ids[i], target)
	}
	_, err = findSession(sc, "agent/nope")
	require.Error(t, err)
	require.Contains(t, err.Error(), "agent/tests", "the error lists what is there")
}

// The Gate is detected from the project when --gate is not given.
func TestSessionsCheckDetectsGate(t *testing.T) {
	repo, _ := sessionsRepo(t)
	var err error
	out := captureStdout(t, func() { err = cmdSessionsCheck([]string{"--repo", repo, "agent/idle"}) })
	require.NoError(t, err, out)
	require.Contains(t, out, "go build ./... && go test ./...")
}

// idOf is the session ID `aoa sessions` gives the worktree at path now.
func idOf(t *testing.T, path string) string {
	t.Helper()
	born, err := worktree.Born(path)
	require.NoError(t, err)
	return sessionID(path, born)
}

// The worktrees aoa makes for its own attempts and Goal branches are on the
// workspace's log already: they are neither listed nor recorded here.
func TestSessionsSkipsAoaWorktrees(t *testing.T) {
	repo, _ := sessionsRepo(t)
	ctx := context.Background()
	_, err := worktree.OpenRepo(repo).AddWorktree(ctx, filepath.Join(t.TempDir(), "attempt"), "aoa/t1-abc123")
	require.NoError(t, err)

	out := captureStdout(t, func() { err = cmdSessions([]string{"--repo", repo}) })
	require.NoError(t, err)
	require.NotContains(t, out, "aoa/t1-abc123")
	require.Contains(t, out, "1 worktrees on aoa/* branches are aoa's own")

	st := sessionsState(t, repo)
	require.Len(t, st.Sessions, 3, "only the three agent sessions are recorded")
	for _, x := range st.Sessions {
		require.NotEqual(t, "aoa/t1-abc123", x.Branch)
	}
}

// A removed session stays on the log but leaves the default table; --all
// brings it back, and the table says how many it left out.
func TestSessionsHidesRemovedUnlessAll(t *testing.T) {
	repo, wt := sessionsRepo(t)
	var err error
	captureStdout(t, func() { err = cmdSessions([]string{"--repo", repo}) })
	require.NoError(t, err)
	gone := idOf(t, wt["login"])
	require.NoError(t, worktree.OpenRepo(repo).Remove(context.Background(), &worktree.Worktree{Path: wt["login"], Branch: "agent/login"}))

	out := captureStdout(t, func() { err = cmdSessions([]string{"--repo", repo}) })
	require.NoError(t, err)
	require.NotContains(t, out, gone)
	require.NotContains(t, out, "worktree gone")
	require.Contains(t, out, "1 removed sessions hidden (--all shows them)")

	out = captureStdout(t, func() { err = cmdSessions([]string{"--repo", repo, "--all"}) })
	require.NoError(t, err)
	require.Contains(t, out, gone)
	require.Contains(t, out, "worktree gone")
	require.NotContains(t, out, "hidden")
}

// A Gate that writes an untracked file of its own (here a coverage profile)
// does not make its own verdict stale, and says what it left behind.
func TestSessionsCheckIgnoresGateOutput(t *testing.T) {
	repo, wt := sessionsRepo(t)
	var err error
	out := captureStdout(t, func() {
		err = cmdSessionsCheck([]string{"--repo", repo, "--gate", "go test -coverprofile=cover.out ./...", "agent/login"})
	})
	require.NoError(t, err, out)
	require.FileExists(t, filepath.Join(wt["login"], "cover.out"))
	require.Contains(t, out, "the Gate left untracked files: cover.out")

	out = captureStdout(t, func() { err = cmdSessions([]string{"--repo", repo}) })
	require.NoError(t, err)
	require.NotContains(t, out, "stale")
	x := sessionsState(t, repo).Sessions[idOf(t, wt["login"])]
	require.NotNil(t, x.Check)
	require.False(t, x.CheckStale(), "the Gate's own output is not a change to the tree")

	// A real edit after the check still makes it stale.
	writeFile(t, filepath.Join(wt["login"], "auth.go"), "package demo\n\nfunc Login() bool { return false }\n")
	out = captureStdout(t, func() { err = cmdSessions([]string{"--repo", repo}) })
	require.NoError(t, err)
	require.Contains(t, out, "pass (stale)")
}

// A worktree removed and re-created at the same path is a new session; the
// old one keeps what it showed.
func TestSessionsRecreatedWorktreeIsANewSession(t *testing.T) {
	repo, wt := sessionsRepo(t)
	var err error
	captureStdout(t, func() { err = cmdSessions([]string{"--repo", repo}) })
	require.NoError(t, err)
	old := idOf(t, wt["login"])

	ctx := context.Background()
	r := worktree.OpenRepo(repo)
	require.NoError(t, r.Remove(ctx, &worktree.Worktree{Path: wt["login"], Branch: "agent/login"}))
	time.Sleep(10 * time.Millisecond) // a distinct birth even on a coarse clock
	_, err = r.AddWorktree(ctx, wt["login"], "agent/login-2")
	require.NoError(t, err)
	fresh := idOf(t, wt["login"])
	require.NotEqual(t, old, fresh)

	out := captureStdout(t, func() { err = cmdSessions([]string{"--repo", repo}) })
	require.NoError(t, err)
	require.Contains(t, out, fresh)

	st := sessionsState(t, repo)
	require.True(t, st.Sessions[old].Removed)
	require.Equal(t, []string{"auth.go"}, st.Sessions[old].Files, "the old session keeps what it did")
	require.False(t, st.Sessions[fresh].Removed)
	require.Equal(t, "agent/login-2", st.Sessions[fresh].Branch)
	require.Empty(t, st.Sessions[fresh].Files)
}

func TestAgoRendersCoarsely(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		when time.Time
		want string
	}{
		{time.Time{}, "—"},
		{now.Add(-30 * time.Second), "just now"},
		{now.Add(-5 * time.Minute), "5m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-72 * time.Hour), "3d ago"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, ago(now, tt.when))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
