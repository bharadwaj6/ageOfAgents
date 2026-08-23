package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
