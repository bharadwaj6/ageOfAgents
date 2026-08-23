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

	text, tokens, model := parseClaudeOutput(envelope)
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
	if _, _, model := parseClaudeOutput(envelope); model != "big" {
		t.Errorf("model = %q, want the model that did the most work", model)
	}
}

// Non-JSON output must degrade to prose rather than blow up, so an older or
// changed CLI still produces a usable Result.
func TestParseClaudeOutputFallsBackToProse(t *testing.T) {
	const prose = "I edited the file.\n```aoa:usage\n{\"tokens\": 42, \"model\": \"m\"}\n```\n"
	text, tokens, model := parseClaudeOutput(prose)
	if text != prose {
		t.Errorf("non-JSON output should pass through verbatim, got %q", text)
	}
	if tokens != 42 || model != "m" {
		t.Errorf("fence fallback = (%d, %q), want (42, \"m\")", tokens, model)
	}
	if _, tk, _ := parseClaudeOutput("just prose"); tk != 0 {
		t.Errorf("unknown usage = %d, want 0 (never invented)", tk)
	}
}

// Subtasks must still be found when the prose is wrapped in the JSON envelope.
func TestParseClaudeOutputFindsSubtasksInsideEnvelope(t *testing.T) {
	env := `{"result":"splitting this up\n` + "```aoa:subtasks\\n" +
		`[{\"local_id\":\"a\",\"title\":\"first\",\"depends_on\":[]}]` + "\\n```" +
		`\n","usage":{"input_tokens":1},"modelUsage":{"m":{"inputTokens":1}}}`
	text, _, _ := parseClaudeOutput(env)
	subs := parseSubtasks(text)
	if len(subs) != 1 || subs[0].Title != "first" {
		t.Fatalf("subtasks = %+v, want one titled \"first\"", subs)
	}
}
