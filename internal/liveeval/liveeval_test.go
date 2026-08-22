package liveeval

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func TestRunEndToEndWithMockBackend(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	task := Task{
		Name:    "marker",
		RepoDir: repo.Dir,
		Goal:    "produce the marker file",
		Gate:    [][]string{{"true"}},
		// The mock writes <ticket-id>.txt; the root ticket for goal g-eval is g-eval-impl.
		Success: [][]string{{"test", "-f", "g-eval-impl.txt"}},
	}

	rep, err := Run(ctx, agent.NewMock(), filepath.Join(base, "ws"), task)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !rep.Success {
		t.Fatalf("expected success, got %+v", rep)
	}
	if len(rep.Violations) != 0 {
		t.Fatalf("unexpected invariant violations: %v", rep.Violations)
	}
	if rep.MAST.Total() != 0 {
		t.Fatalf("clean run should have 0 MAST findings, got %d", rep.MAST.Total())
	}
	if rep.Metrics.Merged == 0 {
		t.Fatal("expected at least one merge")
	}
	if rep.Backend != "mock" {
		t.Errorf("backend = %q, want mock", rep.Backend)
	}
	// The marker really is on main.
	if _, err := os.Stat(filepath.Join(repo.Dir, "g-eval-impl.txt")); err != nil {
		t.Errorf("merged file should be on main: %v", err)
	}
}

func TestLoadTasks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.toml")
	content := `[[task]]
name = "t1"
repo_dir = "/tmp/repo"
goal = "do the thing"
gate = [["go", "build", "./..."]]
success = [["go", "test", "./..."]]
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	tasks, err := LoadTasks(path)
	if err != nil {
		t.Fatalf("LoadTasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Name != "t1" || tasks[0].RepoDir != "/tmp/repo" {
		t.Fatalf("unexpected tasks: %+v", tasks)
	}
	if len(tasks[0].Gate) != 1 || tasks[0].Gate[0][0] != "go" {
		t.Fatalf("unexpected gate: %+v", tasks[0].Gate)
	}
}

// A Gate that always fails, with MaxAttempts 1, makes the rejection terminal so
// the proposal's worktree survives — the path Gate-precision measurement relies
// on. Without recovery here the rejected diff is unrecoverable: the temp base is
// removed as soon as the run returns.
func TestRejectedPatchesRecovered(t *testing.T) {
	requireGit(t)
	ctx := context.Background()
	base := t.TempDir()
	repo, err := worktree.InitRepo(ctx, filepath.Join(base, "repo"))
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	task := Task{
		Name:        "rejected",
		RepoDir:     repo.Dir,
		Goal:        "produce the marker file",
		Gate:        [][]string{{"false"}}, // nothing can pass
		MaxAttempts: 1,                     // so the first rejection is terminal
	}

	rep, err := Run(ctx, agent.NewMock(), filepath.Join(base, "ws"), task)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rep.Metrics.Merged != 0 {
		t.Fatalf("a failing Gate must merge nothing, got %d", rep.Metrics.Merged)
	}
	if len(rep.RejectedPatches) != 1 {
		t.Fatalf("want 1 rejected patch, got %d (%+v)", len(rep.RejectedPatches), rep.RejectedPatches)
	}
	got := rep.RejectedPatches[0]
	if got.Diff == "" {
		t.Error("rejected patch has no diff")
	}
	// The mock writes <ticket>.txt, so the recovered diff must name it.
	if !strings.Contains(got.Diff, "g-eval-impl.txt") {
		t.Errorf("diff should contain the proposal's file, got:\n%s", got.Diff)
	}
	if got.TicketID != "g-eval-impl" {
		t.Errorf("ticket_id = %q, want g-eval-impl", got.TicketID)
	}
}
