// Package worktree manages the git substrate: an integration repository whose
// `main` branch is kept linearizable, and isolated per-ticket worktrees where
// agents do their work (docs/design/adr/002, /003). Workers edit their own worktree;
// the merge queue serializes merges into `main`.
package worktree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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
		// No background auto-gc: keeps git operations deterministic and avoids a
		// detached `git gc --auto` process writing into .git while the workspace
		// is being torn down (which races cleanup under load).
		"-c", "gc.auto=0",
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
	// --template= (empty) keeps the user's global init.templateDir out of a repo
	// that aoa itself creates. Otherwise their hooks land in it: a post-commit
	// that forks a background indexer races teardown, and — worse — a failing
	// pre-commit hook rejects every agent commit, surfacing as the useless
	// "agent produced no changes". An adopted repo keeps its own hooks; those
	// are the user's and legitimately apply.
	if _, err := git(ctx, dir, "init", "--template=", "-b", DefaultBranch); err != nil {
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

// CurrentBranch returns the name of the repo's currently checked-out branch.
func (r *Repo) CurrentBranch(ctx context.Context) (string, error) {
	out, err := git(ctx, r.Dir, "rev-parse", "--abbrev-ref", "HEAD")
	return strings.TrimSpace(out), err
}

// AddWorktree creates a new worktree at dest on a fresh branch cut from the
// integration branch's current tip (HEAD). Cutting from HEAD rather than a fixed
// "main" lets aoa adopt an existing repo on any branch (master, a feature
// branch, …); for a scaffolded repo HEAD is main, so behavior is unchanged.
func (r *Repo) AddWorktree(ctx context.Context, dest, branch string) (*Worktree, error) {
	// Normalize to absolute: MkdirAll resolves against the process CWD while
	// `git worktree add` resolves against the repo dir, so a relative dest would
	// create the directory in two different places (scattering worktrees wherever
	// aoa was launched). Pinning it absolute keeps both consistent.
	dest, err := filepath.Abs(dest)
	if err != nil {
		return nil, fmt.Errorf("resolve worktree path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return nil, fmt.Errorf("create worktree parent: %w", err)
	}
	if _, err := git(ctx, r.Dir, "worktree", "add", "-b", branch, dest, "HEAD"); err != nil {
		return nil, err
	}
	return &Worktree{Path: dest, Branch: branch}, nil
}

// Commit stages all changes in the worktree and commits them. It reports
// changed=false (and no error) when there is nothing to commit.
func (w *Worktree) Commit(ctx context.Context, msg string) (sha string, changed bool, err error) {
	return commitDir(ctx, w.Path, msg)
}

// DiffFromBase returns the patch this worktree's branch adds relative to where
// it diverged from base. Used to recover a proposal the Gate rejected, whose
// commits never reach the integration branch.
func (w *Worktree) DiffFromBase(ctx context.Context, base string) (string, error) {
	mergeBase, err := git(ctx, w.Path, "merge-base", base, "HEAD")
	if err != nil {
		return "", fmt.Errorf("merge-base %s: %w", base, err)
	}
	return git(ctx, w.Path, "diff", strings.TrimSpace(mergeBase)+"..HEAD")
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
// preserving a merge record).
//
// On failure the repository is left exactly as git left it — possibly mid-merge
// with conflict markers on disk. Merge deliberately does not `merge --abort`:
// that call is itself fallible, and silently dropping its error (as this once
// did) meant a failed abort left `main` mid-merge while the caller reported a
// clean rejection. Recovery is the caller's job and must be unconditional —
// mergequeue.Queue restores the pre-merge HEAD with ResetHard, which subsumes
// what an abort would have done and reports its own failure as an error.
func (r *Repo) Merge(ctx context.Context, branch, msg string) (sha string, err error) {
	if _, err := git(ctx, r.Dir, "merge", "--no-ff", "--no-edit", "-m", msg, branch); err != nil {
		return "", err
	}
	out, err := git(ctx, r.Dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// ChangedFiles lists the files branch changes relative to the integration
// branch (the merge base with HEAD), used by the merge queue to detect whether
// two proposals touch disjoint file sets and can be batch-verified together.
func (r *Repo) ChangedFiles(ctx context.Context, branch string) ([]string, error) {
	out, err := git(ctx, r.Dir, "diff", "--name-only", "HEAD..."+branch)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

// ResetHard moves the integration branch back to sha, discarding any changes.
// Used by the merge queue to roll back a merge that failed verification.
func (r *Repo) ResetHard(ctx context.Context, sha string) error {
	_, err := git(ctx, r.Dir, "reset", "--hard", sha)
	return err
}

// Remove tears down a worktree and deletes its branch.
func (r *Repo) Remove(ctx context.Context, w *Worktree) error {
	var err error
	for i := 0; i < 5; i++ {
		_, err = git(ctx, r.Dir, "worktree", "remove", "--force", w.Path)
		if err == nil {
			break
		}
		if strings.Contains(err.Error(), "not a working tree") {
			// Already removed by a previous iteration that partially failed
			err = nil
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	// Best-effort branch delete; "not found" is normal after a merge cleanup.
	// Written as an explicit discard rather than an empty if-branch, which reads
	// as an unfinished thought and is what staticcheck SA9003 flags.
	_, _ = git(ctx, r.Dir, "branch", "-D", w.Branch)
	// Best-effort removal of the now-empty worktree base dir, so a run does not
	// leave behind empty `aoa-worktrees`/`wt` shells. os.Remove only succeeds on an
	// empty dir, so a base still holding sibling worktrees is left untouched.
	_ = os.Remove(filepath.Dir(w.Path))
	return nil
}

// SanitizeBranch turns an arbitrary id into a safe branch component.
func SanitizeBranch(s string) string {
	return strings.NewReplacer("/", "-", " ", "-", "..", "-").Replace(s)
}
