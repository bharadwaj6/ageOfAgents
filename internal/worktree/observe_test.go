package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIsTestPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"auth.go", false},
		{"auth_test.go", true},
		{"internal/auth/auth_test.go", true},
		{"pkg/testdata/golden.json", true},
		{"tests/test_login.py", true},
		{"app/test_login.py", true},
		{"app/login_test.py", true},
		{"conftest.py", true},
		{"sub/conftest.py", true},
		{"pytest.ini", true},
		{"src/Button.test.tsx", true},
		{"src/api.spec.ts", true},
		{"src/__tests__/Button.tsx", true},
		{"src/__snapshots__/Button.tsx.snap", true},
		{"jest.config.js", true},
		{"spec/user_spec.rb", true},
		{"src/main/java/AuthTest.java", true},
		{"src/latest.go", false},
		{"contest.py", false},
		{"docs/testing.md", false},
		{"attest/main.go", false},
		{"Makefile", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsTestPath(tt.path); got != tt.want {
				t.Errorf("IsTestPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestParseWorktreeList(t *testing.T) {
	out := "worktree /repo\nHEAD aaa\nbranch refs/heads/main\n\n" +
		"worktree /repo/.wt/login\nHEAD bbb\nbranch refs/heads/feat/login\n\n" +
		"worktree /repo/.wt/probe\nHEAD ccc\ndetached\n\n" +
		"worktree /tmp/gone\nHEAD ddd\nbranch refs/heads/old\nprunable gitdir file points to non-existent location\n\n"
	got := parseWorktreeList(out)
	want := []Listed{
		{Path: "/repo", Head: "aaa", Branch: "main", Main: true},
		{Path: "/repo/.wt/login", Head: "bbb", Branch: "feat/login"},
		{Path: "/repo/.wt/probe", Head: "ccc"},
		{Path: "/tmp/gone", Head: "ddd", Branch: "old", Prunable: true},
	}
	require.Equal(t, want, got)
}

// Four sessions against main: one committed a change, one touched a test
// without committing, one renamed a test away, and one did nothing yet.
func TestObserveWorktrees(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)
	write(t, filepath.Join(repo.Dir, "auth.go"), "package auth\n")
	write(t, filepath.Join(repo.Dir, "auth_test.go"), "package auth\n")
	_, _, err = repo.CommitAll(ctx, "seed")
	require.NoError(t, err)

	add := func(name string) *Worktree {
		w, err := repo.AddWorktree(ctx, filepath.Join(base, "wt", name), "agent/"+name)
		require.NoError(t, err)
		return w
	}
	committed, touched, renamed, idle := add("committed"), add("touched"), add("renamed"), add("idle")

	write(t, filepath.Join(committed.Path, "auth.go"), "package auth\n\nfunc Login() {}\n")
	_, _, err = committed.Commit(ctx, "feat: login")
	require.NoError(t, err)

	write(t, filepath.Join(touched.Path, "auth_test.go"), "package auth\n\n// assertion deleted\n")
	write(t, filepath.Join(touched.Path, "new_test.go"), "package auth\n")

	_, err = git(ctx, renamed.Path, "mv", "auth_test.go", "auth_helpers.go")
	require.NoError(t, err)
	_, _, err = renamed.Commit(ctx, "refactor: move helpers")
	require.NoError(t, err)

	list, err := ListWorktrees(ctx, repo.Dir)
	require.NoError(t, err)
	require.Len(t, list, 5, "main plus four sessions")
	require.True(t, list[0].Main)
	require.Equal(t, "main", list[0].Branch)

	byBranch := map[string]Observation{}
	for _, wt := range list[1:] {
		o, err := Observe(ctx, wt, "main")
		require.NoError(t, err)
		byBranch[o.Branch] = o
	}

	c := byBranch["agent/committed"]
	require.Equal(t, []string{"auth.go"}, c.Files)
	require.Empty(t, c.TestFiles)
	require.False(t, c.Dirty, "a committed change leaves the tree clean")
	require.NotEmpty(t, c.Base)
	require.False(t, c.LastChange.IsZero())

	tt := byBranch["agent/touched"]
	require.Equal(t, []string{"auth_test.go", "new_test.go"}, tt.Files)
	require.Equal(t, []string{"auth_test.go", "new_test.go"}, tt.TestFiles)
	require.True(t, tt.Dirty)

	r := byBranch["agent/renamed"]
	require.Equal(t, []string{"auth_helpers.go", "auth_test.go"}, r.Files, "a rename lists both names")
	require.Equal(t, []string{"auth_test.go"}, r.TestFiles, "a test moved away still counts as touched")

	i := byBranch["agent/idle"]
	require.Empty(t, i.Files)
	require.False(t, i.Dirty)

	// The fingerprint is stable while nothing changes, and moves when the
	// tree does — which is what makes a recorded check go stale.
	again, err := Observe(ctx, Listed{Path: idle.Path, Branch: "agent/idle"}, "main")
	require.NoError(t, err)
	require.Equal(t, i.Fingerprint, again.Fingerprint)
	write(t, filepath.Join(idle.Path, "auth.go"), "package auth\n\n// edited\n")
	edited, err := Observe(ctx, Listed{Path: idle.Path, Branch: "agent/idle"}, "main")
	require.NoError(t, err)
	require.NotEqual(t, i.Fingerprint, edited.Fingerprint)
	require.Equal(t, []string{"auth.go"}, edited.Files)
}

// An unknown base measures against HEAD instead of failing the observation.
func TestObserveUnknownBase(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)
	w, err := repo.AddWorktree(ctx, filepath.Join(base, "wt"), "agent/x")
	require.NoError(t, err)
	write(t, filepath.Join(w.Path, "x.go"), "package x\n")

	o, err := Observe(ctx, Listed{Path: w.Path, Branch: "agent/x"}, "no-such-branch")
	require.NoError(t, err)
	require.Empty(t, o.Base)
	require.Equal(t, []string{"x.go"}, o.Files)
}

func TestCommonDirIsSharedAcrossWorktrees(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)
	w, err := repo.AddWorktree(ctx, filepath.Join(base, "wt"), "agent/x")
	require.NoError(t, err)

	fromMain, err := CommonDir(ctx, repo.Dir)
	require.NoError(t, err)
	fromWorktree, err := CommonDir(ctx, w.Path)
	require.NoError(t, err)
	require.Equal(t, fromMain, fromWorktree)
	require.True(t, filepath.IsAbs(fromMain))
}

// Tracked ignores untracked files, which Fingerprint counts; Born is fixed
// for a worktree's life and differs once it is re-created at the same path.
func TestObserveTrackedAndBorn(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)
	dir := filepath.Join(base, "wt")
	w, err := repo.AddWorktree(ctx, dir, "agent/x")
	require.NoError(t, err)
	wt := Listed{Path: dir, Branch: "agent/x"}

	before, err := Observe(ctx, wt, "main")
	require.NoError(t, err)
	write(t, filepath.Join(dir, "cover.out"), "mode: set\n")
	withOutput, err := Observe(ctx, wt, "main")
	require.NoError(t, err)
	require.NotEqual(t, before.Fingerprint, withOutput.Fingerprint)
	require.Equal(t, before.Tracked, withOutput.Tracked, "an untracked file is not a tracked change")
	require.Equal(t, []string{"cover.out"}, withOutput.Untracked)

	write(t, filepath.Join(dir, "README.md"), "edited\n")
	edited, err := Observe(ctx, wt, "main")
	require.NoError(t, err)
	require.NotEqual(t, withOutput.Tracked, edited.Tracked)

	born, err := Born(dir)
	require.NoError(t, err)
	again, err := Born(dir)
	require.NoError(t, err)
	require.Equal(t, born, again)

	require.NoError(t, repo.Remove(ctx, w))
	time.Sleep(10 * time.Millisecond)
	_, err = repo.AddWorktree(ctx, dir, "agent/y")
	require.NoError(t, err)
	reborn, err := Born(dir)
	require.NoError(t, err)
	require.NotEqual(t, born, reborn)

	_, err = Born(filepath.Join(base, "nowhere"))
	require.Error(t, err)
}

func write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
