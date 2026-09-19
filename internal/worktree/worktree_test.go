package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func TestInitRepoHasMainCommit(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	repo, err := InitRepo(ctx, filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	head, err := repo.Head(ctx)
	if err != nil || head == "" {
		t.Fatalf("Head: %q err=%v", head, err)
	}
}

func TestWorktreeCommitAndMergeIntoMain(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := InitRepo(ctx, filepath.Join(base, "repo"))
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	wt, err := repo.AddWorktree(ctx, filepath.Join(base, "wt", "t1"), "aoa/t1")
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}

	// Agent writes a file in its isolated worktree.
	if err := os.WriteFile(filepath.Join(wt.Path, "feature.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	sha, changed, err := wt.Commit(ctx, "feat: add feature")
	if err != nil || !changed || sha == "" {
		t.Fatalf("Commit: sha=%q changed=%v err=%v", sha, changed, err)
	}

	// The change must not be on main until merged.
	if _, err := os.Stat(filepath.Join(repo.Dir, "feature.txt")); !os.IsNotExist(err) {
		t.Error("file should not be on main before merge")
	}

	mergeSHA, err := repo.Merge(ctx, "aoa/t1", "merge: t1")
	if err != nil || mergeSHA == "" {
		t.Fatalf("Merge: sha=%q err=%v", mergeSHA, err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, "feature.txt")); err != nil {
		t.Errorf("file should be on main after merge: %v", err)
	}
}

func TestCommitNoChangesReportsUnchanged(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, _ := InitRepo(ctx, filepath.Join(base, "repo"))
	wt, err := repo.AddWorktree(ctx, filepath.Join(base, "wt", "t2"), "aoa/t2")
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	_, changed, err := wt.Commit(ctx, "noop")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if changed {
		t.Error("expected changed=false when nothing was modified")
	}
}

func TestRemoveWorktree(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, _ := InitRepo(ctx, filepath.Join(base, "repo"))
	wt, err := repo.AddWorktree(ctx, filepath.Join(base, "wt", "t3"), "aoa/t3")
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	if err := repo.Remove(ctx, wt); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(wt.Path); !os.IsNotExist(err) {
		t.Error("worktree dir should be gone")
	}
	// The now-empty base dir should be cleaned up too, not left as an empty shell.
	if _, err := os.Stat(filepath.Dir(wt.Path)); !os.IsNotExist(err) {
		t.Error("empty worktree base dir should be removed")
	}
}

func TestSanitizeBranch(t *testing.T) {
	if got := SanitizeBranch("aoa/t 1"); got != "aoa-t-1" {
		t.Errorf("SanitizeBranch = %q", got)
	}
}

// A repo aoa creates must not inherit the user's global git hooks. Their
// post-commit hooks fork background work that races teardown, and a failing
// pre-commit hook would reject every agent commit — surfacing only as the
// useless "agent produced no changes".
func TestInitRepoIgnoresTheGlobalGitTemplate(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	// A template that would install a hook into any repo git initialises.
	tmpl := filepath.Join(t.TempDir(), "template")
	if err := os.MkdirAll(filepath.Join(tmpl, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(tmpl, "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_TEMPLATE_DIR", tmpl)

	// InitRepo commits as part of its work: with the hook inherited, that fails.
	repo, err := InitRepo(ctx, filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatalf("InitRepo inherited the global template's pre-commit hook: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, ".git", "hooks", "pre-commit")); !os.IsNotExist(err) {
		t.Error("a repo aoa creates should start with no inherited hooks")
	}
}

// cloneOfBare makes a bare origin and a clone of it with one commit on main
// pushed, the shape pull-request delivery works against (ADR 016).
func cloneOfBare(t *testing.T) (origin string, repo *Repo) {
	t.Helper()
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	origin = filepath.Join(base, "origin.git")
	if _, err := git(ctx, "", "init", "--bare", "--template=", "-b", DefaultBranch, origin); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	repo, err := InitRepo(ctx, filepath.Join(base, "repo"))
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if _, err := git(ctx, repo.Dir, "remote", "add", "origin", origin); err != nil {
		t.Fatalf("remote add: %v", err)
	}
	if err := repo.Push(ctx, "origin", DefaultBranch); err != nil {
		t.Fatalf("push main: %v", err)
	}
	return origin, repo
}

// revParse resolves ref in dir, failing the test when it does not exist.
func revParse(t *testing.T, dir, ref string) string {
	t.Helper()
	out, err := git(context.Background(), dir, "rev-parse", "--verify", ref)
	if err != nil {
		t.Fatalf("rev-parse %s: %v", ref, err)
	}
	return strings.TrimSpace(out)
}

// commitOn adds a commit touching name on branch in a throwaway worktree cut
// from base, and returns it.
func commitOn(t *testing.T, repo *Repo, branch, base, name string) string {
	t.Helper()
	ctx := context.Background()
	wt, err := repo.AddWorktreeFrom(ctx, filepath.Join(t.TempDir(), "wt"), branch, base)
	if err != nil {
		t.Fatalf("AddWorktreeFrom: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, name), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _, err := wt.Commit(ctx, "feat: "+name)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := git(ctx, repo.Dir, "worktree", "remove", "--force", wt.Path); err != nil {
		t.Fatalf("worktree remove: %v", err)
	}
	return sha
}

func TestDeliveryGitPrimitives(t *testing.T) {
	ctx := context.Background()
	origin, repo := cloneOfBare(t)
	mainSHA := revParse(t, origin, "refs/heads/main")

	// Fetch creates the remote-tracking ref for just that branch.
	require.NoError(t, repo.Fetch(ctx, "origin", "main"))
	require.Equal(t, mainSHA, revParse(t, repo.Dir, "refs/remotes/origin/main"))

	// EnsureBranch cuts the branch once and leaves it alone after that.
	require.NoError(t, repo.EnsureBranch(ctx, "aoa/g1", "refs/remotes/origin/main"))
	require.Equal(t, mainSHA, revParse(t, repo.Dir, "refs/heads/aoa/g1"))
	work := commitOn(t, repo, "aoa/g1-t1", "refs/heads/aoa/g1", "a.txt")
	require.NoError(t, repo.UpdateRef(ctx, "refs/heads/aoa/g1", work, mainSHA))
	require.NoError(t, repo.EnsureBranch(ctx, "aoa/g1", "refs/remotes/origin/main"))
	require.Equal(t, work, revParse(t, repo.Dir, "refs/heads/aoa/g1"), "EnsureBranch must not reset an existing branch")

	// UpdateRef is a compare-and-swap: a stale old value moves nothing.
	require.Error(t, repo.UpdateRef(ctx, "refs/heads/aoa/g1", mainSHA, mainSHA))
	require.Equal(t, work, revParse(t, repo.Dir, "refs/heads/aoa/g1"))

	// Detach checks the branch's commit out without moving any branch.
	tip, err := repo.Detach(ctx, "refs/heads/aoa/g1")
	require.NoError(t, err)
	require.Equal(t, work, tip)
	head, err := repo.CurrentBranch(ctx)
	require.NoError(t, err)
	require.Equal(t, "HEAD", head, "detached")
	require.Equal(t, mainSHA, revParse(t, repo.Dir, "refs/heads/main"))

	// Push publishes the branch; pushing it again is a no-op.
	require.NoError(t, repo.Push(ctx, "origin", "aoa/g1"))
	require.NoError(t, repo.Push(ctx, "origin", "aoa/g1"))
	require.Equal(t, work, revParse(t, origin, "refs/heads/aoa/g1"))
	require.Equal(t, mainSHA, revParse(t, origin, "refs/heads/main"), "origin main untouched")

	// A remote branch that diverged is refused, never overwritten.
	foreign := commitOn(t, repo, "foreign", "refs/heads/main", "b.txt")
	_, err = git(ctx, repo.Dir, "push", "origin", foreign+":refs/heads/aoa/g2")
	require.NoError(t, err)
	require.NoError(t, repo.EnsureBranch(ctx, "aoa/g2", "refs/heads/aoa/g1"))
	err = repo.Push(ctx, "origin", "aoa/g2")
	require.ErrorContains(t, err, "rejected")
	require.Equal(t, foreign, revParse(t, origin, "refs/heads/aoa/g2"))
}
