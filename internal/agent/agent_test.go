package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Compile-time checks that both backends satisfy the interface.
var (
	_ Backend = (*Mock)(nil)
	_ Backend = (*ClaudeCode)(nil)
)

func TestMockWritesPlannedFiles(t *testing.T) {
	wt := t.TempDir()
	m := &Mock{Plan: map[string][]File{
		"impl greeting": {
			{Path: "greeting.go", Content: "package demo\n"},
			{Path: "internal/x/y.txt", Content: "nested\n"},
		},
	}}

	res, err := m.Run(context.Background(), Task{TicketID: "t1", Title: "impl greeting", Worktree: wt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Summary != "impl greeting" {
		t.Errorf("summary = %q", res.Summary)
	}

	for _, f := range []struct{ path, want string }{
		{"greeting.go", "package demo\n"},
		{"internal/x/y.txt", "nested\n"},
	} {
		got, err := os.ReadFile(filepath.Join(wt, f.path))
		if err != nil {
			t.Fatalf("read %s: %v", f.path, err)
		}
		if string(got) != f.want {
			t.Errorf("%s = %q, want %q", f.path, got, f.want)
		}
	}
}

func TestMockDefaultMarkerFile(t *testing.T) {
	wt := t.TempDir()
	m := NewMock()
	if _, err := m.Run(context.Background(), Task{TicketID: "abc", Title: "do thing", Worktree: wt}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(wt, "abc.txt"))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if string(got) != "do thing\n" {
		t.Errorf("marker = %q", got)
	}
}

func TestMockForcedFailure(t *testing.T) {
	m := &Mock{FailTitles: map[string]bool{"bad": true}}
	if _, err := m.Run(context.Background(), Task{TicketID: "t", Title: "bad", Worktree: t.TempDir()}); err == nil {
		t.Error("expected forced failure error")
	}
}

func TestClaudeCodeInvokesInWorktree(t *testing.T) {
	var gotDir, gotBin string
	var gotArgs []string
	c := &ClaudeCode{
		Bin: "claude",
		run: func(_ context.Context, dir, name string, args ...string) (string, error) {
			gotDir, gotBin, gotArgs = dir, name, args
			return "agent output", nil
		},
	}

	task := Task{TicketID: "t1", Title: "add feature", Goal: "ship it", Worktree: "/tmp/wt", Conventions: "use tabs"}
	res, err := c.Run(context.Background(), task)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if gotDir != "/tmp/wt" {
		t.Errorf("dir = %q, want /tmp/wt", gotDir)
	}
	if gotBin != "claude" {
		t.Errorf("bin = %q, want claude", gotBin)
	}
	if len(gotArgs) < 2 || gotArgs[len(gotArgs)-2] != "-p" {
		t.Fatalf("expected -p <prompt>, got %v", gotArgs)
	}
	prompt := gotArgs[len(gotArgs)-1]
	if !strings.Contains(prompt, "add feature") || !strings.Contains(prompt, "use tabs") {
		t.Errorf("prompt missing title/conventions: %q", prompt)
	}
	if res.Trace != "agent output" {
		t.Errorf("trace = %q", res.Trace)
	}
}

func TestBuildPromptIncludesContext(t *testing.T) {
	p := BuildPrompt(Task{Title: "T", Goal: "G", Conventions: "C"})
	for _, want := range []string{"T", "G", "C", "conventions"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
}
