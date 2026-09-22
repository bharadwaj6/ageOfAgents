package mergequeue

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
	"github.com/stretchr/testify/require"
)

// TestOutcomeTreeIdentifiesVerifiedContent is the property the flaky-Gate
// signature rests on: the Tree an Outcome reports names the *content* the Gate
// ran against, not the commit. Two attempts that produce the same patch over the
// same base therefore share a Tree even though their commits differ — so a
// rejection and an acceptance of the same content are comparable by replay.
func TestOutcomeTreeIdentifiesVerifiedContent(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)

	// One attempt, rejected by the Gate; main is rolled back.
	first := proposeFile(t, repo, base, "t1", "feature.txt", "hi\n")
	rejected, err := New(repo, verify.Verifier{Commands: []verify.Command{{"false"}}}).Process(ctx, first)
	require.NoError(t, err)
	require.False(t, rejected.Merged, "gate said no; the proposal must not merge")
	require.NotEmpty(t, rejected.Tree, "a rejection must record what the Gate ran against")

	// The retry writes the identical patch on a fresh branch and commit.
	retry := proposeFile(t, repo, base, "t1-retry", "feature.txt", "hi\n")
	require.NotEqual(t, first.Branch, retry.Branch)
	accepted, err := New(repo, verify.Verifier{Commands: []verify.Command{{"true"}}}).Process(ctx, retry)
	require.NoError(t, err)
	require.True(t, accepted.Merged)

	require.Equal(t, rejected.Tree, accepted.Tree,
		"identical content over an identical base must verify as the same tree")
}

// TestDryRunReportsVerifiedTree: the approval path Gates a candidate twice (dry
// run, then the real merge). Both runs must name the content they checked, or
// the contradiction the approval path can expose stays invisible.
func TestDryRunReportsVerifiedTree(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	require.NoError(t, err)
	p := proposeFile(t, repo, base, "t1", "feature.txt", "hi\n")

	q := New(repo, verify.Verifier{Commands: []verify.Command{{"true"}}})
	dry, err := q.DryRun(ctx, p)
	require.NoError(t, err)
	require.True(t, dry.Verified)
	require.NotEmpty(t, dry.Tree)

	real, err := q.Process(ctx, p)
	require.NoError(t, err)
	require.True(t, real.Merged)
	require.Equal(t, dry.Tree, real.Tree, "nothing moved between the two runs, so the tree is the same")
}
