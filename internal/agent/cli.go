package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
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
var defaultClaudeArgs = []string{"--permission-mode", "acceptEdits", "--output-format", "json", "-p"}

// defaultGrokArgs let the headless grok agent apply its edits, mirroring
// claudecode: without a permission mode the CLI runs but declines to write
// files, so every Task would fail with "agent produced no changes". The worktree
// is the agent's sandbox; the Gate, not the agent, decides what merges.
//
// `--output-format json` is what makes cost accounting real on this backend.
// The CLI reports its own true token counts and model id in that envelope.
var defaultGrokArgs = []string{"--permission-mode", "bypassPermissions", "--output-format", "json", "-p"}

// defaultCodexArgs drive `codex exec`, OpenAI's headless mode.
//
// Two traps, both verified against `codex exec --help` (v0.139.0):
//
//   - There is no "-p" here, and that is deliberate. For codex `-p` is
//     `--profile`; borrowing claude's pattern would pass the entire prompt as a
//     config-profile name. codex takes the prompt as a positional, which the
//     append-last rule already gives us.
//   - `codex exec` defaults to a *read-only* sandbox, so without
//     `--sandbox workspace-write` the agent runs, changes nothing, and every
//     Task fails with "agent produced no changes".
//
// `--json` emits the JSONL event stream parseJSONLStream reads.
var defaultCodexArgs = []string{"exec", "--json", "--sandbox", "workspace-write"}

// defaultCursorArgs drive `cursor-agent` in headless mode.
//
// `-p` here is `--print`, a *boolean* — unlike claude's `-p`, it takes no value.
// The prompt is a positional, which is why `cursor-agent -p "<prompt>"` appears
// to work: the prompt lands as the positional, not as -p's argument.
//
// `--force` allows commands that are not explicitly denied, and `--trust`
// accepts the workspace without prompting: aoa hands the agent a git worktree
// it has never seen, which cursor would otherwise stop to ask about.
//
// Deliberately absent: `-w/--worktree`. cursor would create its own worktree
// under ~/.cursor/worktrees and aoa would find no changes in the one it made.
//
// cursor's JSON envelope carries no token or cost fields at all, so this backend
// reports zero tokens unless the agent emits an aoa:usage fence.
//
// PARTIALLY VERIFIED: every flag above was read off `cursor-agent --help`
// (2026.05.01), but no end-to-end run has been done — the machine this was
// written on has the binary without a logged-in account, and cursor exits with
// "Authentication required" before it reaches the prompt.
var defaultCursorArgs = []string{"-p", "--force", "--trust", "--output-format", "json"}

// defaultGeminiArgs drive Google's `gemini` CLI headless.
//
// UNVERIFIED: gemini is not installed on the machine this was written on, so
// these flags come from the vendor's published reference rather than a live run.
// `-y/--yolo` is deprecated upstream in favour of `--approval-mode yolo`. The
// JSON envelope puts the agent's prose in `response`; its `stats` block is
// documented only as "token usage and API latency metrics", so no token field is
// claimed here — reporting zero beats inventing a field name.
var defaultGeminiArgs = []string{"--approval-mode", "yolo", "--output-format", "json", "-p"}

var ensureGrokLeader sync.Once

// EnsureGrokLeader checks whether the grok leader is reachable and spawns it if
// not: headless grok requires authorization from a leader. It must run only
// after the $PATH check — it starts a daemon that outlives this process, and the
// hermetic suite builds this backend with an empty PATH.
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

// CLI drives a coding-agent CLI as a subprocess inside the task's worktree.
// Every CLI-based harness aoa supports is an instance of this one type; the
// differences between them are data, not code (see cliPresets).
//
// The prompt is always appended as the *final* argv element. That one rule
// covers every harness checked: those that take it as a flag value put the flag
// last in Args ("-p"), and those that take it as a positional simply do not.
// Because it is one element of a []string handed to exec.Command — never a
// shell string — a prompt containing backticks, $(...) or newlines is passed
// through literally. TestCLIPromptIsOneArgvElement locks that in.
type CLI struct {
	name string   // Backend name; also the [pricing] key and the Model fallback
	Bin  string   // binary to invoke
	Args []string // args inserted verbatim before the prompt
	// run executes the command; injectable for tests. dir is the working
	// directory. Defaults to a real exec.CommandContext runner.
	run func(ctx context.Context, dir, name string, args ...string) (string, error)
}

// NewCLI builds a backend for any CLI harness. This is what `[backends.<name>]
// type = "cli"` in aoa.toml reaches: the same type the built-in presets use, so
// a harness aoa has never heard of is not a second-class citizen.
func NewCLI(name, bin string, args []string) *CLI {
	return &CLI{name: name, Bin: bin, Args: args, run: defaultRunner}
}

// Name implements Backend.
func (c *CLI) Name() string { return c.name }

// Run implements Backend: it builds a prompt and invokes the agent in the
// worktree. The agent is expected to edit files in place.
func (c *CLI) Run(ctx context.Context, task Task) (Result, error) {
	runner := c.run
	if runner == nil {
		runner = defaultRunner
	}

	prompt := BuildPrompt(task)
	args := append(append([]string{}, c.Args...), prompt)

	out, err := runner(ctx, task.Worktree, c.Bin, args...)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", c.name, err)
	}
	text, tokens, model := parseCLIOutput(out)
	if model == "" {
		model = c.name
	}
	return Result{
		Trace:    strings.TrimSpace(text),
		Summary:  task.Title,
		Subtasks: parseSubtasks(text),
		Tokens:   tokens,
		Model:    model,
	}, nil
}

// cliPreset is one supported harness, expressed as data.
type cliPreset struct {
	bin  string
	args []string
	// reportsUsage records whether the CLI tells us its own token counts. When
	// it does not, the spend governors cannot work and aoa says so out loud
	// rather than silently reporting $0 (see UsageIsReported).
	reportsUsage bool
	// preflight runs once before the first dispatch, after the $PATH check.
	preflight func()
}

// cliPresets is the list of harnesses aoa drives out of the box. Adding one is
// a row here plus, if it has its own output envelope, a case in parseCLIOutput.
//
// Verification status per row is recorded in docs/harnesses/. Rows marked
// unverified below were built from the vendor's published flags but have not
// been run end to end by this project; `[backends.<name>]` in aoa.toml shadows
// a preset, so a wrong flag can be corrected without waiting for a release.
var cliPresets = map[string]cliPreset{
	"claudecode": {bin: "claude", args: defaultClaudeArgs, reportsUsage: true},
	"grok":       {bin: "grok", args: defaultGrokArgs, reportsUsage: true, preflight: EnsureGrokLeader},
	"codex":      {bin: "codex", args: defaultCodexArgs, reportsUsage: true},
	"cursor":     {bin: "cursor-agent", args: defaultCursorArgs},
	"gemini":     {bin: "gemini", args: defaultGeminiArgs},
}

// CLIPreset returns the built-in backend for name, if there is one.
func CLIPreset(name string) (*CLI, bool) {
	p, ok := cliPresets[name]
	if !ok {
		return nil, false
	}
	return &CLI{name: name, Bin: p.bin, Args: p.args, run: defaultRunner}, true
}

// CLIPresetBin reports the binary a preset needs on $PATH, so callers can check
// for it before running any preflight.
func CLIPresetBin(name string) (string, bool) {
	p, ok := cliPresets[name]
	return p.bin, ok
}

// CLIPresetPreflight returns the preset's one-time setup hook, if any. It must
// only be called after the $PATH check: grok's spawns a detached daemon.
func CLIPresetPreflight(name string) func() {
	return cliPresets[name].preflight
}

// CLINames lists the built-in CLI backends, sorted, for help and error text.
func CLINames() []string {
	out := make([]string, 0, len(cliPresets))
	for name := range cliPresets {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// UsageIsReported says whether a backend tells aoa its own token counts. A
// backend that does not makes max_usd_per_goal and max_tokens_per_goal inert,
// which is worth warning about: a governor that silently does nothing is worse
// than no governor.
func UsageIsReported(name string) bool {
	return cliPresets[name].reportsUsage
}

// cliEnvelope is the union of the single-JSON-object envelopes the supported
// harnesses emit. They report the same concepts under different names — the
// agent's prose is `result` for claude and cursor, `text` for grok, `response`
// for gemini — so one struct with every key, taking the first non-empty, is
// smaller and less brittle than one struct per harness. Assuming the shapes
// matched would have produced silent zeros; naming every key means a harness
// that changes shape degrades to prose rather than lying.
type cliEnvelope struct {
	Result   string `json:"result"`   // claude, cursor
	Text     string `json:"text"`     // grok
	Response string `json:"response"` // gemini
	Usage    struct {
		TotalTokens              int `json:"total_tokens"` // grok reports a total directly
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		InputTokens  int `json:"inputTokens"`
		OutputTokens int `json:"outputTokens"`
		ModelCalls   int `json:"modelCalls"`
	} `json:"modelUsage"`
}

func (e cliEnvelope) text() string {
	for _, v := range []string{e.Result, e.Text, e.Response} {
		if v != "" {
			return v
		}
	}
	return ""
}

// total prefers the CLI's own total when it reports one. Otherwise it sums every
// category the vendor bills for — cache reads and cache creation are real
// spend — so the two paths stay comparable.
func (e cliEnvelope) total() int {
	if e.Usage.TotalTokens > 0 {
		return e.Usage.TotalTokens
	}
	u := e.Usage
	return u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// model returns the busiest model id from modelUsage. One key is the norm; with
// several, the busiest is the one the cost should land against.
func (e cliEnvelope) model() string {
	best, id := -1, ""
	for k, u := range e.ModelUsage {
		n := u.ModelCalls
		if n == 0 {
			n = u.InputTokens + u.OutputTokens
		}
		if n > best {
			best, id = n, k
		}
	}
	return id
}

// parseCLIOutput pulls the agent's prose, its true token count and the model id
// out of whatever a harness printed, trying three shapes in order:
//
//  1. one JSON envelope for the whole run (claude, grok, cursor, gemini);
//  2. a JSONL event stream (codex);
//  3. prose, with the optional aoa:usage fence for self-reported counts.
//
// It degrades rather than fails: an older CLI, or a future format change, lands
// in tier 3 and reports zero tokens. Reporting zero is honest — inventing a
// number is not — and callers render an unknown cost as unknown.
func parseCLIOutput(out string) (text string, tokens int, model string) {
	var env cliEnvelope
	if err := json.Unmarshal([]byte(out), &env); err == nil && env.text() != "" {
		return env.text(), env.total(), env.model()
	}
	if text, tokens, ok := parseJSONLStream(out); ok {
		return text, tokens, ""
	}
	tokens, model = parseUsage(out)
	return out, tokens, model
}

// parseJSONLStream reads codex's `--json` event stream. It scans line by line
// and ignores anything that is not a JSON object: defaultRunner merges stderr
// into stdout, so a whole-buffer unmarshal would be defeated by one warning
// line.
func parseJSONLStream(out string) (text string, tokens int, ok bool) {
	var prose strings.Builder
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev struct {
			Type string `json:"type"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "item.completed":
			if ev.Item.Type == "agent_message" && ev.Item.Text != "" {
				if prose.Len() > 0 {
					prose.WriteString("\n")
				}
				prose.WriteString(ev.Item.Text)
				ok = true
			}
		case "turn.completed":
			// cached_input_tokens is a subset of input_tokens and
			// reasoning_output_tokens of output_tokens, so summing every field
			// the event carries would double-count. These two are the total.
			tokens = ev.Usage.InputTokens + ev.Usage.OutputTokens
			ok = true
		}
	}
	return prose.String(), tokens, ok
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
