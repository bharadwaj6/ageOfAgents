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
