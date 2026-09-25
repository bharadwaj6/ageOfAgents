package worktree

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Listed is one entry of `git worktree list`. Main is the repository's main
// working tree (always listed first); Prunable marks an entry whose directory
// is gone.
type Listed struct {
	Path     string
	Head     string
	Branch   string // short name; empty when detached
	Main     bool
	Bare     bool
	Prunable bool
}

// ListWorktrees returns every working tree of the repository that dir belongs
// to, main first. It reads only; it never prunes or repairs.
func ListWorktrees(ctx context.Context, dir string) ([]Listed, error) {
	out, err := gitStdout(ctx, dir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktreeList(out), nil
}

func parseWorktreeList(out string) []Listed {
	var list []Listed
	var cur *Listed
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := sc.Text()
		key, val, _ := strings.Cut(line, " ")
		switch key {
		case "worktree":
			list = append(list, Listed{Path: val, Main: len(list) == 0})
			cur = &list[len(list)-1]
		case "HEAD":
			if cur != nil {
				cur.Head = val
			}
		case "branch":
			if cur != nil {
				cur.Branch = strings.TrimPrefix(val, "refs/heads/")
			}
		case "bare":
			if cur != nil {
				cur.Bare = true
			}
		case "prunable":
			if cur != nil {
				cur.Prunable = true
			}
		}
	}
	return list
}

// CommonDir returns the absolute path of the git directory shared by every
// worktree of the repository dir belongs to (the main repo's .git).
func CommonDir(ctx context.Context, dir string) (string, error) {
	out, err := gitStdout(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	p := strings.TrimSpace(out)
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return filepath.Abs(p)
}

// Observation is what git and the filesystem show of one working tree
// relative to a base ref. It is gathered by reading only.
type Observation struct {
	Path   string
	Branch string
	Head   string
	// Base is the merge-base of HEAD and the base ref; empty when they share
	// no history, in which case Files are measured against HEAD.
	Base string
	// Files differ from Base: committed, uncommitted and untracked, sorted.
	Files []string
	// TestFiles is the subset of Files that [IsTestPath] matches.
	TestFiles []string
	// Dirty is true when the tree has uncommitted or untracked changes.
	Dirty bool
	// Fingerprint changes whenever the tree's content does.
	Fingerprint string
	// Tracked changes whenever HEAD or the uncommitted patch to tracked files
	// does, and ignores untracked files: it tells an edit apart from output a
	// command dropped next to the sources.
	Tracked string
	// Untracked lists the untracked, non-ignored files, sorted.
	Untracked []string
	// LastChange is the newest of HEAD's commit time and the modification
	// time of every uncommitted or untracked file.
	LastChange time.Time
}

// Observe reads one working tree against baseRef. baseRef may be a branch,
// tag or commit; an empty or unknown one measures against HEAD.
func Observe(ctx context.Context, wt Listed, baseRef string) (Observation, error) {
	o := Observation{Path: wt.Path, Branch: wt.Branch}
	dir := wt.Path

	if out, err := gitStdout(ctx, dir, "rev-parse", "HEAD"); err == nil {
		o.Head = strings.TrimSpace(out)
	}
	if o.Head == "" {
		return o, fmt.Errorf("observe %s: no commit checked out", dir)
	}
	if baseRef != "" {
		if out, err := gitStdout(ctx, dir, "merge-base", baseRef, "HEAD"); err == nil {
			o.Base = strings.TrimSpace(out)
		}
	}
	diffRef := o.Base
	if diffRef == "" {
		diffRef = "HEAD"
	}

	// -z keeps paths raw (no core.quotePath escaping); --no-renames lists a
	// renamed file under both names, so a moved-away test is not hidden.
	vsBase, err := nulList(ctx, dir, "diff", "--name-only", "-z", "--no-renames", "--no-color", diffRef)
	if err != nil {
		return o, fmt.Errorf("observe %s: %w", dir, err)
	}
	vsHead, err := nulList(ctx, dir, "diff", "--name-only", "-z", "--no-renames", "--no-color", "HEAD")
	if err != nil {
		return o, fmt.Errorf("observe %s: %w", dir, err)
	}
	untracked, err := nulList(ctx, dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return o, fmt.Errorf("observe %s: %w", dir, err)
	}
	o.Files = union(vsBase, untracked)
	for _, f := range o.Files {
		if IsTestPath(f) {
			o.TestFiles = append(o.TestFiles, f)
		}
	}
	o.Dirty = len(vsHead) > 0 || len(untracked) > 0

	// The fingerprint covers the commit, the uncommitted patch and the
	// untracked files' size and mtime — enough to see that a tree changed,
	// without reading every untracked file.
	h := sha256.New()
	fmt.Fprintf(h, "head %s\n", o.Head)
	patch, err := gitStdout(ctx, dir, "diff", "--binary", "--no-ext-diff", "--no-textconv", "--no-color", "HEAD")
	if err != nil {
		return o, fmt.Errorf("observe %s: %w", dir, err)
	}
	fmt.Fprintf(h, "patch %d\n%s", len(patch), patch)
	tracked := sha256.New()
	fmt.Fprintf(tracked, "head %s\npatch %d\n%s", o.Head, len(patch), patch)
	o.Tracked = hex.EncodeToString(tracked.Sum(nil))[:16]
	o.Untracked = union(untracked, nil)

	if out, err := gitStdout(ctx, dir, "log", "-1", "--format=%ct", "HEAD"); err == nil {
		if sec, perr := strconv.ParseInt(strings.TrimSpace(out), 10, 64); perr == nil {
			o.LastChange = time.Unix(sec, 0).UTC()
		}
	}
	for _, f := range union(vsHead, untracked) {
		fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			continue // deleted in the tree: it has no mtime
		}
		if mt := fi.ModTime().UTC(); mt.After(o.LastChange) {
			o.LastChange = mt
		}
	}
	for _, f := range untracked {
		fi, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			continue
		}
		fmt.Fprintf(h, "untracked %s %d %d\n", f, fi.Size(), fi.ModTime().UnixNano())
	}
	o.Fingerprint = hex.EncodeToString(h.Sum(nil))[:16]
	return o, nil
}

// Born returns when the linked worktree at dir was created: the modification
// time of its .git file, which `git worktree add` writes once. Two worktrees
// made at the same path at different times therefore have different births.
func Born(dir string) (time.Time, error) {
	fi, err := os.Lstat(filepath.Join(dir, ".git"))
	if err != nil {
		return time.Time{}, fmt.Errorf("worktree %s: %w", dir, err)
	}
	return fi.ModTime().UTC(), nil
}

func nulList(ctx context.Context, dir string, args ...string) ([]string, error) {
	out, err := gitStdout(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	var list []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			list = append(list, p)
		}
	}
	return list, nil
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range [][]string{a, b} {
		for _, p := range l {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	sort.Strings(out)
	return out
}

// testDirs are directory names whose contents are tests or test fixtures.
// Fixtures count: editing a golden file is one way to make a test pass.
var testDirs = map[string]bool{
	"test": true, "tests": true, "__tests__": true, "spec": true,
	"testdata": true, "__snapshots__": true,
}

// IsTestPath reports whether a repo-relative, slash-separated path looks like
// a test, a test fixture, or test-runner configuration, by common convention
// across Go, Python, JavaScript/TypeScript, Ruby and the JVM. It is a
// heuristic on names, not a parse of the project's test setup.
func IsTestPath(p string) bool {
	for _, seg := range strings.Split(path.Dir(p), "/") {
		if testDirs[seg] {
			return true
		}
	}
	base := path.Base(p)
	switch {
	case base == "conftest.py", base == "pytest.ini":
		return true
	case strings.HasSuffix(base, "_test.go"),
		strings.HasSuffix(base, "_test.py"),
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.HasSuffix(base, "_test.rb"),
		strings.HasSuffix(base, "_spec.rb"),
		strings.HasSuffix(base, "Test.java"), strings.HasSuffix(base, "Tests.java"),
		strings.HasSuffix(base, "Test.kt"), strings.HasSuffix(base, "Tests.kt"),
		strings.HasSuffix(base, ".snap"):
		return true
	case strings.Contains(base, ".test."), strings.Contains(base, ".spec."):
		return true
	case strings.HasPrefix(base, "jest.config."), strings.HasPrefix(base, "vitest.config."):
		return true
	}
	return false
}
