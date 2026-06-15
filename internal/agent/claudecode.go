package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// subtaskFence is the fenced-block tag the agent uses to return a decomposition
// instead of editing code. The block body is a JSON array of subtasks.
const subtaskFence = "aoa:subtasks"

// usageFence is an optional fenced block the agent may emit to report token
// usage, e.g. ```aoa:usage {"tokens": 1234} ```. Best-effort: absent or
// unparseable usage simply yields 0, keeping cost accounting opt-in.
const usageFence = "aoa:usage"

// defaultClaudeArgs let the headless agent actually apply its edits. Without a
// permission mode, `claude -p` runs but declines to write files, so every Task
// would fail with "agent produced no changes". acceptEdits auto-approves file
// edits within the worktree (the worktree is the agent's sandbox; the Gate, not
// the agent, decides what merges).
var defaultClaudeArgs = []string{"--permission-mode", "acceptEdits"}

// ClaudeCode drives a real coding agent as a subprocess inside the task's
// worktree. The exact CLI is configurable; by default it invokes
// `claude --permission-mode acceptEdits -p <prompt>` with the worktree as the
// working directory.
type ClaudeCode struct {
	Bin  string   // binary to invoke (default "claude")
	Args []string // extra args inserted before the prompt (default: acceptEdits)
	// run executes the command; injectable for tests. dir is the working
	// directory. Defaults to a real exec.CommandContext runner.
	run func(ctx context.Context, dir, name string, args ...string) (string, error)
}

// NewClaudeCode returns a ClaudeCode backend with default settings, including
// the permission flag that lets the headless agent edit files.
func NewClaudeCode() *ClaudeCode {
	return &ClaudeCode{Bin: "claude", Args: defaultClaudeArgs, run: defaultRunner}
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
	tokens, model := parseUsage(out)
	if model == "" {
		model = c.Name()
	}
	return Result{
		Trace:    strings.TrimSpace(out),
		Summary:  task.Title,
		Subtasks: parseSubtasks(out),
		Tokens:   tokens,
		Model:    model,
	}, nil
}

// parseUsage extracts token usage from an optional "aoa:usage" fenced block
// whose body is JSON like {"tokens": 1234, "model": "..."}. Returns (0, "")
// when absent or unparseable, keeping cost accounting opt-in. The model is
// optional; callers fall back to the backend name when it is empty.
func parseUsage(out string) (int, string) {
	body, ok := fencedBlock(out, usageFence)
	if !ok {
		return 0, ""
	}
	var u struct {
		Tokens int    `json:"tokens"`
		Model  string `json:"model"`
	}
	if err := json.Unmarshal([]byte(body), &u); err != nil {
		return 0, ""
	}
	return u.Tokens, u.Model
}

// parseSubtasks extracts an emergent decomposition from agent output: a fenced
// block tagged "aoa:subtasks" whose body is a JSON array. Returns nil when the
// agent edited code instead (no block, or an unparseable/empty one).
func parseSubtasks(out string) []Subtask {
	body, ok := fencedBlock(out, subtaskFence)
	if !ok {
		return nil
	}
	var raw []struct {
		LocalID        string   `json:"local_id"`
		Title          string   `json:"title"`
		DependsOn      []string `json:"depends_on"`
		IdempotencyKey string   `json:"idempotency_key"`
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil || len(raw) == 0 {
		return nil
	}
	subs := make([]Subtask, 0, len(raw))
	for _, r := range raw {
		subs = append(subs, Subtask{
			LocalID:        r.LocalID,
			Title:          r.Title,
			DependsOn:      r.DependsOn,
			IdempotencyKey: r.IdempotencyKey,
		})
	}
	return subs
}

// fencedBlock returns the body of the first ```<tag> ... ``` fenced block in s.
func fencedBlock(s, tag string) (string, bool) {
	open := "```" + tag
	i := strings.Index(s, open)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(open):]
	// Skip to the end of the opening fence line.
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	}
	j := strings.Index(rest, "```")
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// BuildPrompt assembles the instruction handed to the agent. Conventions are
// included up front as shared coding rules to cut inter-agent
// misalignment (docs/design/adr/006-emergent-task-graph-blackboard.md).
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
		"Keep changes minimal and ensure the project still builds and its tests pass.\n\n")
	b.WriteString("If this task is too large to implement in one focused change, do NOT edit any " +
		"files. Instead, decompose it: output a single fenced block exactly like\n\n")
	b.WriteString("```" + subtaskFence + "\n" +
		`[{"local_id":"types","title":"define shared types","depends_on":[],"idempotency_key":"<goal>:types"},` +
		`{"local_id":"api","title":"implement the API","depends_on":["types"],"idempotency_key":"<goal>:api"}]` +
		"\n```\n\n")
	b.WriteString("Use local_id to reference sibling subtasks in depends_on. Decompose OR implement, " +
		"never both.")
	return b.String()
}

func defaultRunner(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
