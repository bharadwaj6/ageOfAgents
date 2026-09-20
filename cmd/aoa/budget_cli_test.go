package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A workspace can refuse to run unbudgeted. `[budget] require_run_budget` is
// how "nothing runs here without a budget" stops being a habit and becomes
// something the workspace enforces (ADR 017).
func TestRunRefusesWithoutABudgetWhenRequired(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	tmp := t.TempDir()
	if err := cmdInit([]string{"--path", tmp, "--repo", "./demo"}); err != nil {
		t.Fatalf("init: %v", err)
	}
	cfg := "repo = \"./demo\"\nbackend = \"mock\"\nconcurrency = 1\nverify = [[\"true\"]]\n\n[budget]\nrequire_run_budget = true\n"
	if err := os.WriteFile(filepath.Join(tmp, "aoa.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := cmdGoal([]string{"--path", tmp, "add a greeting"}); err != nil {
		t.Fatalf("goal: %v", err)
	}

	err := cmdRun([]string{"--path", tmp})
	if err == nil {
		t.Fatal("cmdRun started without --max-usd in a workspace that requires a budget")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Errorf("want a usage error (exit 2), got %v", err)
	}
	if !strings.Contains(err.Error(), "--max-usd") {
		t.Errorf("the error should name the flag that is missing, got %q", err)
	}
	// Nothing ran: the Goal is still waiting for a budgeted run.
	if _, err := os.Stat(filepath.Join(tmp, ".aoa", "worktrees")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(tmp, ".aoa", "worktrees"))
		if len(entries) != 0 {
			t.Errorf("a refused run dispatched work: %d worktrees", len(entries))
		}
	}
	// With a budget it runs.
	if err := cmdRun([]string{"--path", tmp, "--max-usd", "1"}); err != nil {
		t.Fatalf("run with a budget: %v", err)
	}
}
