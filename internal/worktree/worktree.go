// Package worktree manages the git substrate: an integration repository whose
// `main` branch is kept linearizable, and isolated per-ticket worktrees where
// agents do their work (docs/v2/adr/002, /003). Workers edit their own worktree;
// the merge queue serializes merges into `main`.
package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultBranch is the integration branch kept always-green.
const DefaultBranch = "main"

// Repo is an integration git repository.
type Repo struct {
	Dir string
}

// Worktree is an isolated checkout on its own branch.
type Worktree struct {
	Path   string
	Branch string
}

// git runs a git command with a deterministic, config-independent identity so
// commits/merges work even when the machine has no global git config.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	pre := []string{
		"-c", "user.email=aoa@local",
		"-c", "user.name=Age of Agents",
		"-c", "commit.gpgsign=false",
		"-c", "advice.detachedHead=false",
	}
	cmd := exec.CommandContext(ctx, "git", append(pre, args...)...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// InitRepo creates a fresh integration repo at dir on branch main with one
// initial commit, so worktrees have a base to branch from.
func InitRepo(ctx context.Context, dir string) (*Repo, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create repo dir: %w", err)
	}
	if _, err := git(ctx, dir, "init", "-b", DefaultBranch); err != nil {
		return nil, err
	}
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("# Project\n\nManaged by Age of Agents.\n"), 0o644); err != nil {
		return nil, fmt.Errorf("seed readme: %w", err)
	}
	if _, err := git(ctx, dir, "add", "-A"); err != nil {
		return nil, err
	}
	if _, err := git(ctx, dir, "commit", "-m", "chore: initialize repository"); err != nil {
		return nil, err
	}
	return &Repo{Dir: dir}, nil
}

// OpenRepo wraps an existing repository directory.
func OpenRepo(dir string) *Repo { return &Repo{Dir: dir} }

// Head returns the current commit SHA of the integration branch.
func (r *Repo) Head(ctx context.Context) (string, error) {
	out, err := git(ctx, r.Dir, "rev-parse", "HEAD")
	return strings.TrimSpace(out), err
}

// AddWorktree creates a new worktree at dest on a fresh branch cut from main.
func (r *Repo) AddWorktree(ctx context.Context, dest, branch string) (*Worktree, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, fmt.Errorf("create worktree parent: %w", err)
	}
	if _, err := git(ctx, r.Dir, "worktree", "add", "-b", branch, dest, DefaultBranch); err != nil {
		return nil, err
	}
	return &Worktree{Path: dest, Branch: branch}, nil
}

// Commit stages all changes in the worktree and commits them. It reports
// changed=false (and no error) when there is nothing to commit.
func (w *Worktree) Commit(ctx context.Context, msg string) (sha string, changed bool, err error) {
	return commitDir(ctx, w.Path, msg)
}

// CommitAll stages and commits all changes in the integration repo's main
// worktree (used to seed scaffolding). Reports changed=false when clean.
func (r *Repo) CommitAll(ctx context.Context, msg string) (sha string, changed bool, err error) {
	return commitDir(ctx, r.Dir, msg)
}

// commitDir stages everything in dir and commits, reporting whether anything
// changed.
func commitDir(ctx context.Context, dir, msg string) (sha string, changed bool, err error) {
	if _, err := git(ctx, dir, "add", "-A"); err != nil {
		return "", false, err
	}
	status, err := git(ctx, dir, "status", "--porcelain")
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(status) == "" {
		return "", false, nil
	}
	if _, err := git(ctx, dir, "commit", "-m", msg); err != nil {
		return "", false, err
	}
	out, err := git(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", false, err
	}
	return strings.TrimSpace(out), true, nil
}

// Merge merges the given branch into the integration branch (no fast-forward,
// preserving a merge record). On conflict it aborts and returns an error, so
// `main` is never left in a broken state.
func (r *Repo) Merge(ctx context.Context, branch, msg string) (sha string, err error) {
	if _, err := git(ctx, r.Dir, "merge", "--no-ff", "--no-edit", "-m", msg, branch); err != nil {
		_, _ = git(ctx, r.Dir, "merge", "--abort")
		return "", err
	}
	out, err := git(ctx, r.Dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ResetHard moves the integration branch back to sha, discarding any changes.
// Used by the merge queue to roll back a merge that failed verification.
func (r *Repo) ResetHard(ctx context.Context, sha string) error {
	_, err := git(ctx, r.Dir, "reset", "--hard", sha)
	return err
}

// Remove tears down a worktree and deletes its branch.
func (r *Repo) Remove(ctx context.Context, w *Worktree) error {
	if _, err := git(ctx, r.Dir, "worktree", "remove", "--force", w.Path); err != nil {
		return err
	}
	// Best-effort branch delete; ignore "not found" after a merge cleanup.
	_, _ = git(ctx, r.Dir, "branch", "-D", w.Branch)
	return nil
}

// SanitizeBranch turns an arbitrary id into a safe branch component.
func SanitizeBranch(s string) string {
	return strings.NewReplacer("/", "-", " ", "-", "..", "-").Replace(s)
}
