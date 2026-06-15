package agent

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// defaultGrokArgs let the headless grok agent apply its edits, mirroring
// claudecode: without acceptEdits the CLI runs but declines to write files, so
// every Task would fail with "agent produced no changes". The worktree is the
// agent's sandbox; the Gate, not the agent, decides what merges.
var defaultGrokArgs = []string{"--permission-mode", "bypassPermissions"}

var ensureGrokLeader sync.Once

// EnsureGrokLeader checks if the grok leader is reachable and spawns it if not.
// This is needed because headless grok requires authorization from a leader.
func EnsureGrokLeader() {
	ensureGrokLeader.Do(func() {
		cmd := exec.Command("grok", "leader", "list")
		if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "(Reachable)") {
			_ = exec.Command("grok", "agent", "leader").Start()
		}
	})
}

// Grok drives xAI's `grok` CLI as a subprocess inside the task's worktree. The
// CLI authenticates via the user's local grok.com login (no API key), and its
// flags are Claude Code-compatible, so this backend is the claudecode backend
// pointed at a different binary: it invokes `grok --permission-mode acceptEdits
// -p <prompt>` with the worktree as the working directory and lets the agent
// edit files in place. The Scheduler commits, verifies, and merges as usual.
type Grok struct {
	Bin  string   // binary to invoke (default "grok")
	Args []string // extra args inserted before the prompt (default: acceptEdits)
	// run executes the command; injectable for tests. dir is the working
	// directory. Defaults to a real exec.CommandContext runner.
	run func(ctx context.Context, dir, name string, args ...string) (string, error)
}

// NewGrok returns a Grok backend with default settings, including the
// permission flag that lets the headless agent edit files.
func NewGrok() *Grok {
	return &Grok{Bin: "grok", Args: defaultGrokArgs, run: defaultRunner}
}

// Name implements Backend.
func (*Grok) Name() string { return "grok" }

// Run implements Backend: it builds a prompt and invokes the grok CLI in the
// worktree. The agent is expected to edit files in place.
func (g *Grok) Run(ctx context.Context, task Task) (Result, error) {
	runner := g.run
	if runner == nil {
		runner = defaultRunner
	}
	bin := g.Bin
	if bin == "" {
		bin = "grok"
	}

	prompt := BuildPrompt(task)
	args := append(append([]string{}, g.Args...), "-p", prompt)

	out, err := runner(ctx, task.Worktree, bin, args...)
	if err != nil {
		return Result{}, fmt.Errorf("grok: %w", err)
	}
	_ = os.WriteFile(filepath.Join(task.Worktree, "grok_transcript.log"), []byte(out), 0o644)

	tokens, model := parseUsage(out)
	if model == "" {
		model = g.Name()
	}
	return Result{
		Trace:    strings.TrimSpace(out),
		Summary:  task.Title,
		Subtasks: parseSubtasks(out),
		Tokens:   tokens,
		Model:    model,
	}, nil
}
