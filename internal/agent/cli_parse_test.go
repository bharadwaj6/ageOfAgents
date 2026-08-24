package agent

import "testing"

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

	text, tokens, model := parseCLIOutput(envelope)
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
}

func TestParseClaudeOutputPicksBusiestModel(t *testing.T) {
	const envelope = `{"result":"x","usage":{"input_tokens":1},
	  "modelUsage":{"small":{"inputTokens":1,"outputTokens":1},
	                "big":{"inputTokens":900,"outputTokens":100}}}`
	if _, _, model := parseCLIOutput(envelope); model != "big" {
		t.Errorf("model = %q, want the model that did the most work", model)
	}
}

// Non-JSON output must degrade to prose rather than blow up, so an older or
// changed CLI still produces a usable Result.
func TestParseClaudeOutputFallsBackToProse(t *testing.T) {
	const prose = "I edited the file.\n```aoa:usage\n{\"tokens\": 42, \"model\": \"m\"}\n```\n"
	text, tokens, model := parseCLIOutput(prose)
	if text != prose {
		t.Errorf("non-JSON output should pass through verbatim, got %q", text)
	}
	if tokens != 42 || model != "m" {
		t.Errorf("fence fallback = (%d, %q), want (42, \"m\")", tokens, model)
	}
	if _, tk, _ := parseCLIOutput("just prose"); tk != 0 {
		t.Errorf("unknown usage = %d, want 0 (never invented)", tk)
	}
}

// Subtasks must still be found when the prose is wrapped in the JSON envelope.
func TestParseClaudeOutputFindsSubtasksInsideEnvelope(t *testing.T) {
	env := `{"result":"splitting this up\n` + "```aoa:subtasks\\n" +
		`[{\"local_id\":\"a\",\"title\":\"first\",\"depends_on\":[]}]` + "\\n```" +
		`\n","usage":{"input_tokens":1},"modelUsage":{"m":{"inputTokens":1}}}`
	text, _, _ := parseCLIOutput(env)
	subs := parseSubtasks(text)
	if len(subs) != 1 || subs[0].Title != "first" {
		t.Fatalf("subtasks = %+v, want one titled \"first\"", subs)
	}
}

// Shape captured from `codex exec --json` (v0.139.0), interleaved with a stderr
// line: defaultRunner uses CombinedOutput, so the scan has to survive one.
func TestParseCLIOutputReadsCodexJSONL(t *testing.T) {
	const stream = `{"type":"thread.started","thread_id":"t_01"}
[2026-08-24T10:00:00] warning: something on stderr
{"type":"item.completed","item":{"type":"reasoning","text":"thinking"}}
{"type":"item.completed","item":{"type":"agent_message","text":"added the tests"}}
{"type":"turn.completed","usage":{"input_tokens":20000,"cached_input_tokens":18000,"output_tokens":500,"reasoning_output_tokens":400}}`

	text, tokens, model := parseCLIOutput(stream)
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

	text, tokens, _ := parseCLIOutput(envelope)
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

	text, tokens, _ := parseCLIOutput(envelope)
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

	_, tokens, model := parseCLIOutput(out)
	if tokens != 4321 || model != "mycoder-1" {
		t.Errorf("fence not honoured: tokens=%d model=%q", tokens, model)
	}
}
