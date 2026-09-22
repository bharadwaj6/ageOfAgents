package agent

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// Real usage, not self-reported usage. Before this the backend asked parseUsage
// for an `aoa:usage` fence that BuildPrompt never requested, so every real run
// reported 0 tokens and the spend governor, --max-cost and every $ column were
// silently inert on the repo's flagship real backend.
func TestParseClaudeOutputReadsRealUsage(t *testing.T) {
	// Shape captured from a live `claude --output-format json` run. Note it
	// differs from grok's envelope: prose is in "result", not "text", and there
	// is no total_tokens field at all.
	const envelope = `{
	  "is_error": false,
	  "result": "done: added the tests",
	  "total_cost_usd": 0.2969205,
	  "usage": {
	    "input_tokens": 2,
	    "output_tokens": 4,
	    "cache_creation_input_tokens": 29275,
	    "cache_read_input_tokens": 8121
	  },
	  "modelUsage": {"claude-opus-5": {"inputTokens": 2, "outputTokens": 4}}
	}`

	text, tokens, model, cost := parseCLIOutput(envelope, "")
	if text != "done: added the tests" {
		t.Errorf("text = %q, want the agent's prose from `result`", text)
	}
	// Cache reads and cache creation are billed spend, and counting them keeps
	// this comparable with grok's own total_tokens.
	if want := 2 + 4 + 29275 + 8121; tokens != want {
		t.Errorf("tokens = %d, want %d (every billed category)", tokens, want)
	}
	if model != "claude-opus-5" {
		t.Errorf("model = %q, want the id from modelUsage", model)
	}
	// The harness's own cost is what gets charged, in preference to pricing.
	if cost != 0.2969205 {
		t.Errorf("cost = %v, want total_cost_usd 0.2969205", cost)
	}
}

func TestParseClaudeOutputPicksBusiestModel(t *testing.T) {
	const envelope = `{"result":"x","usage":{"input_tokens":1},
	  "modelUsage":{"small":{"inputTokens":1,"outputTokens":1},
	                "big":{"inputTokens":900,"outputTokens":100}}}`
	if _, _, model, _ := parseCLIOutput(envelope, ""); model != "big" {
		t.Errorf("model = %q, want the model that did the most work", model)
	}
}

// Non-JSON output must degrade to prose rather than blow up, so an older or
// changed CLI still produces a usable Result.
func TestParseClaudeOutputFallsBackToProse(t *testing.T) {
	const prose = "I edited the file.\n```aoa:usage\n{\"tokens\": 42, \"model\": \"m\"}\n```\n"
	text, tokens, model, _ := parseCLIOutput(prose, "")
	if text != prose {
		t.Errorf("non-JSON output should pass through verbatim, got %q", text)
	}
	if tokens != 42 || model != "m" {
		t.Errorf("fence fallback = (%d, %q), want (42, \"m\")", tokens, model)
	}
	if _, tk, _, _ := parseCLIOutput("just prose", ""); tk != 0 {
		t.Errorf("unknown usage = %d, want 0 (never invented)", tk)
	}
}

// Subtasks must still be found when the prose is wrapped in the JSON envelope.
func TestParseClaudeOutputFindsSubtasksInsideEnvelope(t *testing.T) {
	env := `{"result":"splitting this up\n` + "```aoa:subtasks\\n" +
		`[{\"local_id\":\"a\",\"title\":\"first\",\"depends_on\":[]}]` + "\\n```" +
		`\n","usage":{"input_tokens":1},"modelUsage":{"m":{"inputTokens":1}}}`
	text, _, _, _ := parseCLIOutput(env, "")
	subs := parseSubtasks(text)
	if len(subs) != 1 || subs[0].Title != "first" {
		t.Fatalf("subtasks = %+v, want one titled \"first\"", subs)
	}
}

// Shape captured from `codex exec --json` (v0.139.0), interleaved with a stray
// non-JSON line, which the scan has to survive.
func TestParseCLIOutputReadsCodexJSONL(t *testing.T) {
	const stream = `{"type":"thread.started","thread_id":"t_01"}
[2026-08-24T10:00:00] warning: something on stderr
{"type":"item.completed","item":{"type":"reasoning","text":"thinking"}}
{"type":"item.completed","item":{"type":"agent_message","text":"added the tests"}}
{"type":"turn.completed","usage":{"input_tokens":20000,"cached_input_tokens":18000,"output_tokens":500,"reasoning_output_tokens":400}}`

	text, tokens, model, _ := parseCLIOutput(stream, "")
	if text != "added the tests" {
		t.Errorf("text = %q, want only the agent_message prose", text)
	}
	// cached_input_tokens is a subset of input_tokens and
	// reasoning_output_tokens of output_tokens; summing all four double-counts.
	if tokens != 20500 {
		t.Errorf("tokens = %d, want 20500 (input+output, no double-counting)", tokens)
	}
	if model != "" {
		t.Errorf("model = %q — the codex stream carries no model id, so the caller's fallback must win", model)
	}
}

// cursor's envelope has no token or cost fields at all. Reporting zero is the
// honest answer; it is also why `aoa` warns that the spend governors are inert
// on this backend.
func TestParseCLIOutputReadsCursorEnvelope(t *testing.T) {
	const envelope = `{"type":"result","subtype":"success","is_error":false,
	  "duration_ms":42000,"result":"refactored the parser","session_id":"s_1"}`

	text, tokens, _, _ := parseCLIOutput(envelope, "")
	if text != "refactored the parser" {
		t.Errorf("text = %q, want the `result` field", text)
	}
	if tokens != 0 {
		t.Errorf("tokens = %d, want 0 — cursor reports none, and inventing one is worse", tokens)
	}
}

// gemini puts the prose in `response`. Its `stats` block is documented only as
// "token usage and API latency metrics" with no field names published, so no
// token count is claimed until someone runs it and contributes a real fixture.
func TestParseCLIOutputReadsGeminiEnvelope(t *testing.T) {
	const envelope = `{"session_id":"s_1","response":"updated the docs","stats":{}}`

	text, tokens, _, _ := parseCLIOutput(envelope, "")
	if text != "updated the docs" {
		t.Errorf("text = %q, want the `response` field", text)
	}
	if tokens != 0 {
		t.Errorf("tokens = %d, want 0 until a real gemini stats fixture exists", tokens)
	}
}

// A harness that reports nothing can still opt in to cost accounting through the
// aoa:usage fence — which is what makes BYOHarness backends first-class.
func TestParseCLIOutputFallsBackToTheUsageFence(t *testing.T) {
	out := "did the thing\n\n```" + usageFence + "\n{\"tokens\": 4321, \"model\": \"mycoder-1\"}\n```\n"

	_, tokens, model, _ := parseCLIOutput(out, "")
	if tokens != 4321 || model != "mycoder-1" {
		t.Errorf("fence not honoured: tokens=%d model=%q", tokens, model)
	}
}

// A harness that prints its envelope on stdout and anything at all on stderr
// must still be charged what the envelope reports. Shape captured from agy
// (Antigravity CLI 1.2.8), which warned on stderr and was charged 0 tokens.
func TestCLIRunChargesEnvelopeDespiteStderr(t *testing.T) {
	const envelope = `{"conversation_id":"x","status":"SUCCESS","response":"","duration_seconds":3,"num_turns":1,` +
		`"usage":{"input_tokens":18457,"output_tokens":280,"thinking_tokens":208,"cache_read_tokens":0,"total_tokens":18737},` +
		`"denied_actions":[{"action":"command","display_name":"RunCommand"}]}`
	const warning = "jetski: no output produced — a tool required the \"command\" permission\n"
	exit := errors.New("exit status 1")
	for _, tc := range []struct {
		name   string
		runErr error
	}{
		{"success", nil},
		{"non-zero exit", exit},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCLI("agy", "agy", nil)
			c.run = func(context.Context, string, string, ...string) (string, string, error) {
				return envelope + "\n", warning, tc.runErr
			}
			res, err := c.Run(context.Background(), Task{Title: "t"})
			if !errors.Is(err, tc.runErr) {
				t.Fatalf("err = %v, want %v", err, tc.runErr)
			}
			if res.Tokens != 18737 {
				t.Errorf("tokens = %d, want 18737 from the stdout envelope", res.Tokens)
			}
			if tc.runErr != nil && !strings.Contains(err.Error(), "jetski: no output produced") {
				t.Errorf("err = %q, want it to carry the stderr warning", err)
			}
		})
	}
}

// defaultRunner must hand back stdout and stderr apart: merging them is what
// made one stderr line invalidate the envelope.
func TestDefaultRunnerSeparatesStderr(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH")
	}
	stdout, stderr, err := defaultRunner(context.Background(), t.TempDir(), "sh", "-c",
		`echo '{"result":"ok","usage":{"total_tokens":7}}'; echo warning >&2`)
	if err != nil {
		t.Fatalf("defaultRunner: %v", err)
	}
	if _, tokens, _, _ := parseCLIOutput(stdout, stderr); tokens != 7 {
		t.Errorf("tokens = %d, want 7 (stdout %q, stderr %q)", tokens, stdout, stderr)
	}
	if strings.TrimSpace(stderr) != "warning" {
		t.Errorf("stderr = %q, want the warning alone", stderr)
	}
}

// The prose tier still sees stderr, after stdout, so nothing a harness said is
// lost from the trace.
func TestParseCLIOutputProseIncludesStderr(t *testing.T) {
	text, _, _, _ := parseCLIOutput("did it", "warn")
	if text != "did it\nwarn" {
		t.Errorf("text = %q, want stdout then stderr", text)
	}
}

// The harness's own figures are what an attempt is charged, whether it ended
// well or not (ADR 017). Before this a non-zero exit returned an empty Result,
// so every errored attempt charged nothing; an envelope with an empty `result`
// fell through to prose and charged nothing either; and total_cost_usd was
// ignored everywhere.
func TestCLIRunReportsTheHarnessesSpend(t *testing.T) {
	const usage = `"total_cost_usd": 0.42, "usage": {"input_tokens": 10, "output_tokens": 5},
	  "modelUsage": {"claude-sonnet-5": {"inputTokens": 10, "outputTokens": 5}}`
	tests := []struct {
		name    string
		out     string
		runErr  error
		wantErr bool
	}{
		{name: "success", out: `{"is_error": false, "result": "done", ` + usage + `}`},
		{name: "non-zero exit", out: `{"is_error": true, "result": "hit max turns", ` + usage + `}`,
			runErr: errors.New("exit status 1"), wantErr: true},
		{name: "empty result", out: `{"is_error": true, "result": "", ` + usage + `}`},
		{name: "empty result, non-zero exit", out: `{"is_error": true, "result": "", ` + usage + `}`,
			runErr: errors.New("exit status 1"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewCLI("claudecode", "claude", nil)
			c.run = func(context.Context, string, string, ...string) (string, string, error) { return tt.out, "", tt.runErr }
			res, err := c.Run(context.Background(), Task{TicketID: "t1", Title: "x", Worktree: "/wt"})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if res.Tokens != 15 || res.Model != "claude-sonnet-5" || res.CostUSD != 0.42 {
				t.Errorf("spend = (%d tokens, %q, $%v), want (15, claude-sonnet-5, $0.42)", res.Tokens, res.Model, res.CostUSD)
			}
		})
	}
}

// A non-zero exit must carry the harness's own explanation, not just
// "exit status 1": the message of a JSON error envelope when there is one,
// else the tail of the output, bounded either way.
func TestCLIRunFailureCarriesHarnessMessage(t *testing.T) {
	exit := errors.New("exit status 1")
	for _, tc := range []struct {
		name, out string
		want      string
		maxLen    int
	}{
		{"json envelope", `{"type":"error","message":"Not signed in. Run grok login"}` + "\nError: Not signed in.\n",
			"grok: exit status 1: Not signed in. Run grok login", 0},
		{"stderr tail", "warming up\nError: quota exceeded\n", "grok: exit status 1: warming up\nError: quota exceeded", 0},
		{"no output", "", "grok: exit status 1", 0},
		{"bounded", strings.Repeat("x", 5000), "", len("grok: exit status 1: ") + maxFailureDetail + len("[...truncated...]\n") + 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewCLI("grok", "grok", nil)
			c.run = func(context.Context, string, string, ...string) (string, string, error) { return tc.out, "", exit }
			_, err := c.Run(context.Background(), Task{Title: "t"})
			if err == nil || !errors.Is(err, exit) {
				t.Fatalf("err = %v, want it to wrap the exit error", err)
			}
			if tc.want != "" && err.Error() != tc.want {
				t.Errorf("err = %q, want %q", err.Error(), tc.want)
			}
			if tc.maxLen > 0 && len(err.Error()) > tc.maxLen {
				t.Errorf("err is %d bytes, want <= %d", len(err.Error()), tc.maxLen)
			}
		})
	}
}
