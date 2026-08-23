package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// defaultGrokArgs let the headless grok agent apply its edits, mirroring
// claudecode: without acceptEdits the CLI runs but declines to write files, so
// every Task would fail with "agent produced no changes". The worktree is the
// agent's sandbox; the Gate, not the agent, decides what merges.
//
// `--output-format json` is what makes cost accounting real on this backend.
// The CLI reports its own true token counts and model id in that envelope;
// without it stdout is prose and the only fallback is asking the model to
// self-report, which produces a confident invented number. A fabricated cost is
// worse than a missing one.
var defaultGrokArgs = []string{"--permission-mode", "bypassPermissions", "--output-format", "json"}

var ensureGrokLeader sync.Once

// EnsureGrokLeader checks if the grok leader is reachable and spawns it if not.
// This is needed because headless grok requires authorization from a leader.
func EnsureGrokLeader() {
	ensureGrokLeader.Do(func() {
		cmd := exec.Command("grok", "leader", "list")
		if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "(Reachable)") {
			// Spawn a detached leader daemon. It outlives this process by design
			// (the CLI needs a persistent leader); surface a spawn failure rather
			// than swallow it, so a missing/broken `grok` binary is visible.
			if startErr := exec.Command("grok", "agent", "leader").Start(); startErr != nil {
				fmt.Fprintf(os.Stderr, "grok: could not start leader daemon: %v\n", startErr)
			}
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
	// The transcript is returned in Result.Trace (and persisted to the Event Log);
	// we deliberately do not drop a grok_transcript.log into the worktree, where the
	// orchestrator's `git add -A` would commit the agent's scratch into the proposal.

	text, tokens, model := parseGrokOutput(out)
	if model == "" {
		model = g.Name()
	}
	return Result{
		Trace:    strings.TrimSpace(text),
		Summary:  task.Title,
		Subtasks: parseSubtasks(text),
		Tokens:   tokens,
		Model:    model,
	}, nil
}

// grokEnvelope is the subset of `grok --output-format json` we consume.
type grokEnvelope struct {
	Text  string `json:"text"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		ModelCalls int `json:"modelCalls"`
	} `json:"modelUsage"`
}

// parseGrokOutput pulls the agent's prose, its true token count and the model id
// out of the CLI's JSON envelope. It degrades rather than fails: output that
// isn't the expected JSON (an older CLI, or a future format change) is treated
// as plain prose, falling back to the optional aoa:usage fence. Reporting zero
// tokens is honest — inventing a number is not — and callers render an unknown
// cost as unknown.
func parseGrokOutput(out string) (text string, tokens int, model string) {
	var env grokEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil || env.Text == "" {
		tokens, model = parseUsage(out)
		return out, tokens, model
	}
	// modelUsage is keyed by model id. One key is the norm; with several, take
	// the busiest so the cost lands against the model that did the work.
	best := -1
	for id, u := range env.ModelUsage {
		if u.ModelCalls > best {
			best, model = u.ModelCalls, id
		}
	}
	return env.Text, env.Usage.TotalTokens, model
}
