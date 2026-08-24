package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Re-running quickstart over a live workspace would scaffold a second demo repo
// on top of one that may already hold real history. Refuse, and say what to run
// instead.
func TestQuickstartRefusesAnExistingWorkspace(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "aoa.toml"), []byte("backend = \"mock\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := cmdQuickstart([]string{"--path", root})
	if err == nil {
		t.Fatal("quickstart overwrote an existing workspace")
	}
	if !strings.Contains(err.Error(), "aoa run") {
		t.Errorf("the error should point at the command to run instead, got %q", err)
	}
}

func TestQuoteOnlyQuotesWhatNeedsIt(t *testing.T) {
	for _, tc := range []struct {
		argv []string
		want string
	}{
		{[]string{"goal", "--path", "/tmp/ws"}, "goal --path /tmp/ws"},
		{[]string{"goal", "add a greeting"}, `goal "add a greeting"`},
		{[]string{"run", ""}, `run ""`},
	} {
		if got := quote(tc.argv); got != tc.want {
			t.Errorf("quote(%q) = %q, want %q", tc.argv, got, tc.want)
		}
	}
}
