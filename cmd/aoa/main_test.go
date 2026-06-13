package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// TestEndToEnd exercises the real CLI entry points (init → goal → run) against a
// temp town using the default offline mock backend, asserting a verified merge.
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
