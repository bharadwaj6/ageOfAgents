package mergequeue

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
	"github.com/stretchr/testify/require"
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
	repo, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)
	preHead, err := repo.Head(ctx)
	require.NoError(t, err)
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
	postHead, err := repo.Head(ctx)
	require.NoError(t, err)
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
	repo, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)
	preHead, err := repo.Head(ctx)
	require.NoError(t, err)
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
	postHead, err := repo.Head(ctx)
	require.NoError(t, err)
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
	repo, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)
	preHead, err := repo.Head(ctx)
	require.NoError(t, err)
	p := proposeFile(t, repo, base, "t1", "feature.txt", "hi\n")

	q := New(repo, verify.Verifier{Commands: []verify.Command{{"false"}}})
	out, err := q.DryRun(ctx, p)
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if out.Verified || out.Merged {
		t.Fatalf("failing gate should not verify or merge, got %+v", out)
	}
	postHead, err := repo.Head(ctx)
	require.NoError(t, err)
	if postHead != preHead {
		t.Errorf("main must be unchanged after a failed dry run: pre=%s post=%s", preHead, postHead)
	}
}

func TestProcessRecordsRegressionEscape(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)
	p := proposeFile(t, repo, base, "t1", "feature.txt", "hi\n")

	// Gate passes; the broader Shadow set fails. The merge is kept (the Gate is
	// the contract), but flagged as a regression the Gate's blind spot let through.
	q := New(repo, verify.Verifier{Commands: []verify.Command{{"true"}}})
	q.Shadow = verify.Verifier{Commands: []verify.Command{{"false"}}}
	out, err := q.Process(ctx, p)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if !out.Merged {
		t.Fatalf("Gate passed, expected merge, got %+v", out)
	}
	if !out.RegressionEscaped {
		t.Error("Shadow failed but RegressionEscaped was not set")
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, "feature.txt")); err != nil {
		t.Errorf("merge must be kept despite the shadow failure: %v", err)
	}
}

func TestProcessNoEscapeWhenShadowPasses(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)
	p := proposeFile(t, repo, base, "t1", "feature.txt", "hi\n")

	q := New(repo, verify.Verifier{Commands: []verify.Command{{"true"}}})
	q.Shadow = verify.Verifier{Commands: []verify.Command{{"true"}}}
	out, err := q.Process(ctx, p)
	require.NoError(t, err)
	if !out.Merged || out.RegressionEscaped {
		t.Fatalf("shadow passes → merged with no escape; got %+v", out)
	}
}

func TestProcessBatchMergesDisjointTogether(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)

	// Three proposals touching disjoint files — all should merge in one batch.
	a := proposeFile(t, repo, base, "a", "a.txt", "A\n")
	b := proposeFile(t, repo, base, "b", "b.txt", "B\n")
	c := proposeFile(t, repo, base, "c", "c.txt", "C\n")

	q := New(repo, verify.Verifier{Commands: []verify.Command{{"true"}}})
	outs, err := q.ProcessBatch(ctx, []Proposal{a, b, c})
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	for i, o := range outs {
		if !o.Merged || !o.Verified {
			t.Errorf("outcome[%d] = %+v, want merged+verified", i, o)
		}
	}
	for _, f := range []string{"a.txt", "b.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(repo.Dir, f)); err != nil {
			t.Errorf("%s should be on main: %v", f, err)
		}
	}
}

func TestProcessBatchSerializesOverlapping(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)

	// Two proposals both create dup.txt (overlapping files) → they cannot batch.
	// Processed serially: the first lands; the second textually conflicts and is
	// rejected (main stays coherent), exactly as the single-Process path behaves.
	a := proposeFile(t, repo, base, "a", "dup.txt", "from-a\n")
	b := proposeFile(t, repo, base, "b", "dup.txt", "from-b\n")

	q := New(repo, verify.Verifier{Commands: []verify.Command{{"true"}}})
	outs, err := q.ProcessBatch(ctx, []Proposal{a, b})
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	merged := 0
	for _, o := range outs {
		if o.Merged {
			merged++
		}
	}
	if merged != 1 {
		t.Fatalf("overlapping proposals: want exactly 1 merged, got %d (%+v)", merged, outs)
	}
	got, err := os.ReadFile(filepath.Join(repo.Dir, "dup.txt"))
	require.NoError(t, err)
	if string(got) != "from-a\n" {
		t.Errorf("main dup.txt = %q, want a's content (first wins)", got)
	}
}

func TestProcessBatchGateFailureIsolatesCulprit(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)

	// Gate fails when a "BAD" marker file exists. a is clean, b is poison; their
	// files are disjoint so they batch, the union fails the Gate → rollback →
	// serial: a merges, b is rejected (the culprit is isolated).
	a := proposeFile(t, repo, base, "a", "good.txt", "ok\n")
	b := proposeFile(t, repo, base, "b", "BAD", "poison\n")
	gate := verify.Verifier{Commands: []verify.Command{{"sh", "-c", "! test -f BAD"}}}

	q := New(repo, gate)
	outs, err := q.ProcessBatch(ctx, []Proposal{a, b})
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	if !outs[0].Merged {
		t.Errorf("clean proposal a should merge after isolation, got %+v", outs[0])
	}
	if outs[1].Merged {
		t.Errorf("poison proposal b should be rejected, got %+v", outs[1])
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, "good.txt")); err != nil {
		t.Errorf("a's file should be on main: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, "BAD")); !os.IsNotExist(err) {
		t.Error("b's poison file must not remain on main")
	}
}

func TestProcessRejectsOnMergeConflict(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)

	// Two proposals editing the same file from the same base → second conflicts.
	a := proposeFile(t, repo, base, "a", "README.md", "version A\n")
	b := proposeFile(t, repo, base, "b", "README.md", "version B\n")

	q := New(repo, verify.Verifier{Commands: []verify.Command{{"true"}}})

	out, err := q.Process(ctx, a)
	require.NoError(t, err)
	if !out.Merged {
		t.Fatalf("first proposal should merge, got %+v", out)
	}
	out, err = q.Process(ctx, b)
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
	got, err := os.ReadFile(filepath.Join(repo.Dir, "README.md"))
	require.NoError(t, err)
	if string(got) != "version A\n" {
		t.Errorf("main README = %q, want A", got)
	}
}
