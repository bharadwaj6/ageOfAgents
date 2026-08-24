package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Compile-time checks that the backends satisfy the interface.
var (
	_ Backend = (*Mock)(nil)
	_ Backend = (*CLI)(nil)
	_ Backend = (*Anthropic)(nil)
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

func TestBuildPromptIncludesContext(t *testing.T) {
	p := BuildPrompt(Task{Title: "T", Goal: "G", Conventions: "C"})
	for _, want := range []string{"T", "G", "C", "conventions", subtaskFence} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
}

func TestBuildPromptIncludesPriorFailure(t *testing.T) {
	// A first attempt carries no failure context.
	first := BuildPrompt(Task{Title: "T", Attempt: 1})
	if strings.Contains(first, "previous attempt") {
		t.Errorf("first attempt should not mention a previous failure:\n%s", first)
	}

	// A retry surfaces the prior gate output so the agent can fix the cause.
	retry := BuildPrompt(Task{Title: "T", Attempt: 2, LastFailure: "FAIL: TestThing\nundefined: foo"})
	for _, want := range []string{"attempt 2", "undefined: foo", "Previous Gate output"} {
		if !strings.Contains(retry, want) {
			t.Errorf("retry prompt missing %q:\n%s", want, retry)
		}
	}
}

func TestBuildPromptIncludesDependencyContext(t *testing.T) {
	p := BuildPrompt(Task{Title: "T", DepContext: "- define shared User type (commit abc123)\n"})
	for _, want := range []string{"Completed dependencies", "define shared User type"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
	// No dependency context => no dependencies section.
	if strings.Contains(BuildPrompt(Task{Title: "T"}), "Completed dependencies") {
		t.Error("prompt should omit the dependencies section when there is no context")
	}
}

func TestTailLinesKeepsBoundedTail(t *testing.T) {
	if got := tailLines("short", 100); got != "short" {
		t.Errorf("tailLines(short) = %q, want unchanged", got)
	}
	big := strings.Repeat("a\n", 5000) // far longer than maxFailureChars
	got := tailLines(big, maxFailureChars)
	if len(got) > maxFailureChars+len("[...truncated...]\n") {
		t.Errorf("tailLines length = %d, want <= bound", len(got))
	}
	if !strings.HasPrefix(got, "[...truncated...]") {
		t.Errorf("truncated output should be marked, got prefix %q", got[:20])
	}
}

// capture returns a CLI whose runner records how it was invoked and replays out.
func capture(name string, out string, got *[]string, dir, bin *string) *CLI {
	c, _ := CLIPreset(name)
	if c == nil {
		c = NewCLI(name, name, nil)
	}
	c.run = func(_ context.Context, d, b string, args ...string) (string, error) {
		*dir, *bin, *got = d, b, args
		return out, nil
	}
	return c
}

// One table replaces the per-backend invocation tests. Adding a harness is a row
// here, not a new test function — which is the point of making presets data.
func TestCLIPresetArgv(t *testing.T) {
	task := Task{TicketID: "t1", Title: "add feature", Goal: "ship it", Worktree: "/tmp/wt", Conventions: "use tabs"}
	want := BuildPrompt(task)

	for _, tc := range []struct {
		backend string
		bin     string
		// mustContain are flags this harness cannot work without; each has a
		// comment on the corresponding defaultXArgs saying why.
		mustContain []string
	}{
		{"claudecode", "claude", []string{"--permission-mode", "acceptEdits"}},
		{"grok", "grok", []string{"--permission-mode", "bypassPermissions"}},
		{"codex", "codex", []string{"exec", "--sandbox", "workspace-write"}},
		{"cursor", "cursor-agent", []string{"-p", "--force", "--trust"}},
		{"gemini", "gemini", []string{"--approval-mode", "yolo"}},
	} {
		t.Run(tc.backend, func(t *testing.T) {
			var gotArgs []string
			var gotDir, gotBin string
			c := capture(tc.backend, "agent output", &gotArgs, &gotDir, &gotBin)

			res, err := c.Run(context.Background(), task)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if gotDir != task.Worktree {
				t.Errorf("dir = %q, want %q", gotDir, task.Worktree)
			}
			if gotBin != tc.bin {
				t.Errorf("bin = %q, want %q", gotBin, tc.bin)
			}
			if len(gotArgs) == 0 {
				t.Fatal("no args passed")
			}
			// The prompt is always the final argv element — the one rule that
			// lets flag-value and positional harnesses share this type.
			if got := gotArgs[len(gotArgs)-1]; got != want {
				t.Errorf("last arg is not the prompt:\n got %q\nwant %q", got, want)
			}
			joined := strings.Join(gotArgs[:len(gotArgs)-1], " ")
			for _, flag := range tc.mustContain {
				if !strings.Contains(joined, flag) {
					t.Errorf("%s args missing %q: %v", tc.backend, flag, gotArgs)
				}
			}
			if res.Trace != "agent output" {
				t.Errorf("trace = %q", res.Trace)
			}
			if res.Model != tc.backend {
				t.Errorf("model = %q, want the backend name as fallback", res.Model)
			}
		})
	}
}

// codex's `-p` is --profile, not the prompt, and cursor's is a boolean --print.
// Borrowing claude's "-p <prompt>" pattern for either would silently pass the
// whole prompt as a flag value. Lock in that neither gets a trailing -p.
func TestCLIPresetsDoNotAssumeDashPMeansPrompt(t *testing.T) {
	for _, name := range []string{"codex", "cursor"} {
		c, ok := CLIPreset(name)
		if !ok {
			t.Fatalf("no preset for %q", name)
		}
		if n := len(c.Args); n > 0 && c.Args[n-1] == "-p" {
			t.Errorf("%s must not end with -p: for codex that is --profile, and for cursor a boolean", name)
		}
	}
}

// The prompt is handed to exec.Command as one []string element, never through a
// shell. This invariant is load-bearing — a Goal is user text — and had no
// coverage before.
func TestCLIPromptIsOneArgvElement(t *testing.T) {
	task := Task{
		TicketID: "t1",
		Title:    "fix `id`; rm -rf /; $(whoami) \"quoted\" && echo pwned",
		Goal:     "line one\nline two",
		Worktree: "/tmp/wt",
	}
	var gotArgs []string
	var gotDir, gotBin string
	c := capture("claudecode", "ok", &gotArgs, &gotDir, &gotBin)

	if _, err := c.Run(context.Background(), task); err != nil {
		t.Fatalf("Run: %v", err)
	}
	last := gotArgs[len(gotArgs)-1]
	if last != BuildPrompt(task) {
		t.Fatal("the prompt was altered on its way to the CLI")
	}
	if !strings.Contains(last, "rm -rf /") || !strings.Contains(last, "$(whoami)") {
		t.Error("shell metacharacters must survive verbatim, not be escaped or stripped")
	}
	for _, a := range gotArgs[:len(gotArgs)-1] {
		if strings.Contains(a, "rm -rf") {
			t.Errorf("prompt text leaked into another argv element: %q", a)
		}
	}
}

// The transcript belongs in Result.Trace and the Event Log. Anything written
// into the worktree would be swept up by the orchestrator's `git add -A` and
// committed into the proposal.
func TestCLILeavesNoScratchInWorktree(t *testing.T) {
	wt := t.TempDir()
	c := NewCLI("claudecode", "claude", nil)
	c.run = func(context.Context, string, string, ...string) (string, error) {
		return "agent output", nil
	}
	if _, err := c.Run(context.Background(), Task{TicketID: "t1", Title: "x", Worktree: wt}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	entries, err := os.ReadDir(wt)
	if err != nil {
		t.Fatalf("read worktree: %v", err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("backend wrote scratch into the worktree: %v", names)
	}
}

func TestCLIParsesSubtaskDecomposition(t *testing.T) {
	out := "Decomposing.\n\n```" + subtaskFence + "\n" +
		`[{"local_id":"types","title":"shared types","depends_on":[],"idempotency_key":"g:types"},` +
		`{"local_id":"api","title":"the API","depends_on":["types"],"idempotency_key":"g:api"}]` +
		"\n```\n"
	c := NewCLI("grok", "grok", nil)
	c.run = func(context.Context, string, string, ...string) (string, error) { return out, nil }

	res, err := c.Run(context.Background(), Task{TicketID: "t1", Title: "big task", Worktree: "/wt"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Subtasks) != 2 {
		t.Fatalf("got %d subtasks, want 2", len(res.Subtasks))
	}
	if res.Subtasks[1].LocalID != "api" || len(res.Subtasks[1].DependsOn) != 1 || res.Subtasks[1].DependsOn[0] != "types" {
		t.Errorf("second subtask malformed: %+v", res.Subtasks[1])
	}
}

func TestCLINoSubtasksWhenImplementing(t *testing.T) {
	c := NewCLI("claudecode", "claude", nil)
	c.run = func(context.Context, string, string, ...string) (string, error) {
		return "Edited three files and ran the tests.", nil
	}
	res, err := c.Run(context.Background(), Task{TicketID: "t1", Title: "small task", Worktree: "/wt"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Subtasks) != 0 {
		t.Errorf("plain prose must not decompose: %+v", res.Subtasks)
	}
}

func TestCLINamesAreSortedAndComplete(t *testing.T) {
	got := CLINames()
	want := []string{"claudecode", "codex", "cursor", "gemini", "grok"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("CLINames() = %v, want %v", got, want)
	}
}
