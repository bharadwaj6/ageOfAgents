package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// ClaudeCode drives a real coding agent as a subprocess inside the task's
// worktree. The exact CLI is configurable; by default it invokes `claude -p
// <prompt>` with the worktree as the working directory.
type ClaudeCode struct {
	Bin  string   // binary to invoke (default "claude")
	Args []string // extra args inserted before the prompt
	// run executes the command; injectable for tests. dir is the working
	// directory. Defaults to a real exec.CommandContext runner.
	run func(ctx context.Context, dir, name string, args ...string) (string, error)
}

// NewClaudeCode returns a ClaudeCode backend with default settings.
func NewClaudeCode() *ClaudeCode {
	return &ClaudeCode{Bin: "claude", run: defaultRunner}
}

// Name implements Backend.
func (*ClaudeCode) Name() string { return "claudecode" }

// Run implements Backend: it builds a prompt and invokes the agent in the
// worktree. The agent is expected to edit files in place.
func (c *ClaudeCode) Run(ctx context.Context, task Task) (Result, error) {
	runner := c.run
	if runner == nil {
		runner = defaultRunner
	}
	bin := c.Bin
	if bin == "" {
		bin = "claude"
	}

	prompt := BuildPrompt(task)
	args := append(append([]string{}, c.Args...), "-p", prompt)

	out, err := runner(ctx, task.Worktree, bin, args...)
	if err != nil {
		return Result{}, fmt.Errorf("claudecode: %w", err)
	}
	return Result{Trace: strings.TrimSpace(out), Summary: task.Title}, nil
}

// BuildPrompt assembles the instruction handed to the agent. Conventions are
// included up front as shared coding rules to cut inter-agent
// misalignment (docs/v2/adr/006-emergent-task-graph-blackboard.md).
func BuildPrompt(task Task) string {
	var b strings.Builder
	if task.Conventions != "" {
		b.WriteString("Project conventions (follow exactly):\n")
		b.WriteString(task.Conventions)
		b.WriteString("\n\n")
	}
	if task.Goal != "" {
		fmt.Fprintf(&b, "Overall goal: %s\n\n", task.Goal)
	}
	fmt.Fprintf(&b, "Task: %s\n\n", task.Title)
	b.WriteString("Make the necessary code changes in this working directory. " +
		"Keep changes minimal and ensure the project still builds and its tests pass.")
	return b.String()
}

func defaultRunner(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
