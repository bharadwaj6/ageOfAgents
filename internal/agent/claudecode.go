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

// maxFailureChars bounds how much prior-attempt Gate output is fed back into a
// retry prompt, keeping token cost predictable. The tail is kept because build
// and test failures report the actual error at the end of their output.
const maxFailureChars = 2000

// defaultClaudeArgs let the headless agent actually apply its edits. Without a
// permission mode, `claude -p` runs but declines to write files, so every Task
// would fail with "agent produced no changes". acceptEdits auto-approves file
// edits within the worktree (the worktree is the agent's sandbox; the Gate, not
// the agent, decides what merges).
//
// `--output-format json` is what makes cost accounting real on this backend.
// The CLI reports its own true token counts, cost and model id; without it
// stdout is prose and the only fallback is asking the model to self-report,
// which produces a confident invented number. A fabricated cost is worse than
// a missing one.
var defaultClaudeArgs = []string{"--permission-mode", "acceptEdits", "--output-format", "json"}

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
	text, tokens, model := parseClaudeOutput(out)
	if model == "" {
		model = c.Name()
	}
	return Result{
		Trace:    strings.TrimSpace(text),
		Summary:  task.Title,
		Subtasks: parseSubtasks(text),
		Tokens:   tokens,
		Model:    model,
	}, nil
}

// claudeEnvelope is the subset of `claude --output-format json` we consume.
// Deliberately not shared with grokEnvelope: the two CLIs report the same
// concepts under different names — claude puts the agent's prose in `result`
// (grok uses `text`) and reports no total at all, so the total has to be summed
// here. Guessing that the shapes matched would have produced silent zeros.
type claudeEnvelope struct {
	Result  string `json:"result"`
	IsError bool   `json:"is_error"`
	Usage   struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
	} `json:"modelUsage"`
}

// total sums every token category the CLI bills for. Cache reads and cache
// creation are real spend, and including them matches what grok reports as its
// own `total_tokens`, so the two backends stay comparable.
func (e claudeEnvelope) total() int {
	u := e.Usage
	return u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// parseClaudeOutput pulls the agent's prose, its true token count and the model
// id out of the CLI's JSON envelope. Output that isn't that JSON (an older CLI,
// or a future format change) is treated as plain prose, falling back to the
// optional aoa:usage fence. Reporting zero is honest; inventing a number is not.
func parseClaudeOutput(out string) (text string, tokens int, model string) {
	var env claudeEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil || env.Result == "" {
		tokens, model = parseUsage(out)
		return out, tokens, model
	}
	// modelUsage is keyed by model id. One key is the norm; with several, take
	// the busiest so the cost lands against the model that did the work.
	best := -1
	for id, u := range env.ModelUsage {
		if n := u.InputTokens + u.OutputTokens; n > best {
			best, model = n, id
		}
	}
	return env.Result, env.total(), model
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
	if task.DepContext != "" {
		b.WriteString("Completed dependencies you can build on:\n")
		b.WriteString(task.DepContext)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "Task: %s\n\n", task.Title)
	if task.Attempt > 1 && task.LastFailure != "" {
		fmt.Fprintf(&b, "This is attempt %d. Your previous attempt failed the verification Gate "+
			"with the output below. Diagnose the cause and fix it; do not repeat the same mistake.\n\n",
			task.Attempt)
		b.WriteString("Previous Gate output:\n")
		b.WriteString(tailLines(task.LastFailure, maxFailureChars))
		b.WriteString("\n\n")
	}
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

// tailLines returns at most max trailing bytes of s, trimmed to a line boundary
// and prefixed with a truncation marker when content was dropped. Build/test
// output puts the actual failure last, so the tail is the useful part.
func tailLines(s string, max int) string {
	if len(s) <= max {
		return s
	}
	tail := s[len(s)-max:]
	if nl := strings.IndexByte(tail, '\n'); nl >= 0 && nl+1 < len(tail) {
		tail = tail[nl+1:]
	}
	return "[...truncated...]\n" + tail
}

func defaultRunner(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
