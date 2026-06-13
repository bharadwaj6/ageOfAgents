package mergequeue

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

// proposeFile creates a worktree, writes a file, commits, and returns the proposal.
func proposeFile(t *testing.T, repo *worktree.Repo, base, ticket, path, content string) Proposal {
	t.Helper()
	ctx := context.Background()
	branch := "aoa/" + ticket
	wt, err := repo.AddWorktree(ctx, filepath.Join(base, "wt", ticket), branch)
	if err != nil {
		t.Fatalf("AddWorktree: %v", err)
	}
	// Tear down the worktree before t.TempDir's RemoveAll so the cross-linked
	// .git/worktrees admin files don't race cleanup under parallel test load.
	t.Cleanup(func() { _ = repo.Remove(context.Background(), wt) })
	if err := os.WriteFile(filepath.Join(wt.Path, path), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, changed, err := wt.Commit(ctx, "feat: "+ticket); err != nil || !changed {
		t.Fatalf("Commit: changed=%v err=%v", changed, err)
	}
	return Proposal{TicketID: ticket, Worker: "w", Branch: branch}
}

func TestProcessMergesWhenVerifierPasses(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	p := proposeFile(t, repo, base, "t1", "feature.txt", "hi\n")

	q := New(repo, verify.Verifier{Commands: []verify.Command{{"true"}}})
	out, err := q.Process(ctx, p)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !out.Merged || out.MergeCommit == "" {
		t.Fatalf("expected merged, got %+v", out)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, "feature.txt")); err != nil {
		t.Errorf("merged file should be on main: %v", err)
	}
}

func TestProcessRollsBackWhenVerifierFails(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, _ := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	preHead, _ := repo.Head(ctx)
	p := proposeFile(t, repo, base, "t1", "feature.txt", "hi\n")

	q := New(repo, verify.Verifier{Commands: []verify.Command{{"false"}}})
	out, err := q.Process(ctx, p)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if out.Merged {
		t.Fatal("expected rejection")
	}
	if out.Reason == "" {
		t.Error("expected a failure reason")
	}
	postHead, _ := repo.Head(ctx)
	if postHead != preHead {
		t.Errorf("main should be rolled back: pre=%s post=%s", preHead, postHead)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, "feature.txt")); !os.IsNotExist(err) {
		t.Error("rejected file must not remain on main")
	}
}

func TestDryRunVerifiesWithoutWritingToMain(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, _ := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	preHead, _ := repo.Head(ctx)
	p := proposeFile(t, repo, base, "t1", "feature.txt", "hi\n")

	q := New(repo, verify.Verifier{Commands: []verify.Command{{"true"}}})
	out, err := q.DryRun(ctx, p)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if !out.Verified {
		t.Fatalf("expected the candidate to verify, got %+v", out)
	}
	if out.Merged {
		t.Error("a dry run must never report Merged")
	}
	postHead, _ := repo.Head(ctx)
	if postHead != preHead {
		t.Errorf("main must be unchanged after a dry run: pre=%s post=%s", preHead, postHead)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, "feature.txt")); !os.IsNotExist(err) {
		t.Error("dry-run candidate must not remain on main")
	}
}

func TestDryRunReportsUnverifiedWhenGateFails(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, _ := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	preHead, _ := repo.Head(ctx)
	p := proposeFile(t, repo, base, "t1", "feature.txt", "hi\n")

	q := New(repo, verify.Verifier{Commands: []verify.Command{{"false"}}})
	out, err := q.DryRun(ctx, p)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if out.Verified || out.Merged {
		t.Fatalf("failing gate should not verify or merge, got %+v", out)
	}
	postHead, _ := repo.Head(ctx)
	if postHead != preHead {
		t.Errorf("main must be unchanged after a failed dry run: pre=%s post=%s", preHead, postHead)
	}
}

func TestProcessRejectsOnMergeConflict(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, _ := worktree.InitRepo(ctx, filepath.Join(base, "repo"))

	// Two proposals editing the same file from the same base → second conflicts.
	a := proposeFile(t, repo, base, "a", "README.md", "version A\n")
	b := proposeFile(t, repo, base, "b", "README.md", "version B\n")

	q := New(repo, verify.Verifier{Commands: []verify.Command{{"true"}}})

	if out, _ := q.Process(ctx, a); !out.Merged {
		t.Fatalf("first proposal should merge, got %+v", out)
	}
	out, err := q.Process(ctx, b)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if out.Merged {
		t.Error("conflicting proposal should be rejected")
	}
	if out.Reason == "" {
		t.Error("expected conflict reason")
	}
	// main remains coherent (A's content).
	got, _ := os.ReadFile(filepath.Join(repo.Dir, "README.md"))
	if string(got) != "version A\n" {
		t.Errorf("main README = %q, want A", got)
	}
}
