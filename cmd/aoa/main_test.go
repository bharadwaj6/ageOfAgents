package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/config"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// TestEndToEnd exercises the real CLI entry points (init → goal → run) against a
// temp workspace using the default offline mock backend, asserting a verified merge.
func TestEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	tmp := t.TempDir()

	if err := cmdInit([]string{"--path", tmp, "--repo", "./demo"}); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Scaffolding landed on main.
	for _, f := range []string{"go.mod", "doc.go"} {
		if _, err := os.Stat(filepath.Join(tmp, "demo", f)); err != nil {
			t.Fatalf("init should scaffold %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, "aoa.toml")); err != nil {
		t.Fatalf("init should write aoa.toml: %v", err)
	}

	if err := cmdGoal([]string{"--path", tmp, "Add", "a", "greeting"}); err != nil {
		t.Fatalf("goal: %v", err)
	}
	if err := cmdRun([]string{"--path", tmp}); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Fold the log and assert the goal's ticket merged.
	led, err := ledger.Open(filepath.Join(tmp, ".aoa", "events.jsonl"))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	events, err := led.Read()
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	s, err := state.Fold(events)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if !s.Settled() {
		t.Fatal("expected work to settle")
	}
	merged := 0
	for _, tk := range s.Tickets {
		if tk.Status == state.StatusMerged {
			merged++
		}
	}
	if merged != 1 {
		t.Fatalf("expected exactly 1 merged ticket, got %d", merged)
	}

	// The Merged event must be present in the log.
	var sawMerged bool
	for _, e := range events {
		if e.Type == api.Merged {
			sawMerged = true
		}
	}
	if !sawMerged {
		t.Error("expected a Merged event in the log")
	}
}

func TestDetectGate(t *testing.T) {
	cases := []struct {
		marker, lang, want string
	}{
		{"go.mod", "go", "go build ./... && go test ./..."},
		{"package.json", "node", "npm test"},
		{"pyproject.toml", "python", "python -m pytest"},
		{"Makefile", "make", "make test"},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, c.marker), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		gate, lang := detectGate(dir)
		if lang != c.lang {
			t.Errorf("%s: lang = %q, want %q", c.marker, lang, c.lang)
		}
		if got := gateString(gate); got != c.want {
			t.Errorf("%s: gate = %q, want %q", c.marker, got, c.want)
		}
	}
	if gate, _ := detectGate(t.TempDir()); gate != nil {
		t.Errorf("unrecognized project: gate = %v, want nil", gate)
	}
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// adoptableRepo builds a minimal real Go module on a non-default branch.
func adoptableRepo(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "myrepo")
	r, err := worktree.InitRepo(ctx, dir)
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	// Neutralize any global git hooks (this environment ships a post-checkout
	// hook that force-reverts checkouts to `main`) so the test can establish and
	// keep a feature branch. A local core.hooksPath applies to every git
	// operation on this repo, including the orchestrator's.
	runGit(t, dir, "config", "core.hooksPath", t.TempDir())
	writeRepoFile(t, dir, "go.mod", "module myrepo\n\ngo "+goVersion()+"\n")
	writeRepoFile(t, dir, "doc.go", "package myrepo\n")
	if _, _, err := r.CommitAll(ctx, "init module"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	runGit(t, dir, "checkout", "-b", "feature")
	return dir
}

func TestInitAdopt(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoDir := adoptableRepo(t)
	ws := t.TempDir()

	if err := cmdInit([]string{"--path", ws, "--adopt", repoDir}); err != nil {
		t.Fatalf("init --adopt: %v", err)
	}
	cfg, err := config.Load(filepath.Join(ws, "aoa.toml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Repo != repoDir {
		t.Errorf("Repo = %q, want adopted absolute path %q", cfg.Repo, repoDir)
	}
	if got := gateString(cfg.Verify); got != "go build ./... && go test ./..." {
		t.Errorf("Verify = %q, want the auto-detected go gate", got)
	}
	// Adoption must not scaffold into the user's repo.
	if _, err := os.Stat(filepath.Join(repoDir, "CONVENTIONS.md")); err == nil {
		t.Error("adopt must not write CONVENTIONS.md into the adopted repo")
	}
	// Re-running must not clobber an existing config without --force.
	if err := cmdInit([]string{"--path", ws, "--adopt", repoDir}); err == nil {
		t.Error("re-init without --force should refuse to overwrite aoa.toml")
	}
	if err := cmdInit([]string{"--path", ws, "--adopt", repoDir, "--force"}); err != nil {
		t.Errorf("re-init --force should succeed: %v", err)
	}

	// Rejects a non-git directory.
	if err := cmdInit([]string{"--path", t.TempDir(), "--adopt", t.TempDir()}); err == nil {
		t.Error("adopt of a non-git directory should error")
	}
}

func TestAdoptedRepoRunsOnFeatureBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repoDir := adoptableRepo(t) // on branch "feature", not main
	ws := t.TempDir()

	if err := cmdInit([]string{"--path", ws, "--adopt", repoDir}); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if err := cmdGoal([]string{"--path", ws, "add", "a", "marker"}); err != nil {
		t.Fatalf("goal: %v", err)
	}
	if err := cmdRun([]string{"--path", ws}); err != nil {
		t.Fatalf("run on adopted feature branch: %v", err)
	}

	// The repo stayed on its feature branch and the work merged there.
	if branch := strings.TrimSpace(runGit(t, repoDir, "rev-parse", "--abbrev-ref", "HEAD")); branch != "feature" {
		t.Errorf("repo branch = %q, want feature", branch)
	}
	led, _ := ledger.Open(filepath.Join(ws, ".aoa", "events.jsonl"))
	events, _ := led.Read()
	s, _ := state.Fold(events)
	merged := 0
	for _, tk := range s.Tickets {
		if tk.Status == state.StatusMerged {
			merged++
		}
	}
	if merged != 1 {
		t.Fatalf("expected 1 merged ticket on the feature branch, got %d", merged)
	}
}

func TestFilterEvents(t *testing.T) {
	mk := func(typ api.EventType) api.Event { return api.Event{Type: typ} }
	events := []api.Event{
		mk(api.GoalSubmitted), mk(api.TicketCreated), mk(api.Merged), mk(api.TicketCreated),
	}
	if got := filterEvents(events, ""); len(got) != 4 {
		t.Errorf("empty filter should pass all 4, got %d", len(got))
	}
	got := filterEvents(events, string(api.TicketCreated))
	if len(got) != 2 {
		t.Fatalf("type filter: want 2 TicketCreated, got %d", len(got))
	}
	for _, e := range got {
		if e.Type != api.TicketCreated {
			t.Errorf("filtered event has wrong type %q", e.Type)
		}
	}
	// Filtering doesn't mutate the input slice.
	if events[2].Type != api.Merged {
		t.Error("filterEvents must not modify the source slice")
	}
}
