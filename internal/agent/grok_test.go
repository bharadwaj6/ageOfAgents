package agent

import "testing"

// Real usage, not self-reported usage. The grok CLI knows its own token counts;
// before this the backend asked parseUsage for an `aoa:usage` fence that
// BuildPrompt never requested, so every real run reported 0 tokens and the spend
// governor, --max-cost and every $ column were silently inert.
func TestParseGrokOutputReadsRealUsage(t *testing.T) {
	// Shape captured from `grok --output-format json` (v1.0.5).
	const envelope = `{
	  "text": "done: added the tests",
	  "stopReason": "end_turn",
	  "usage": {"input_tokens": 20392, "output_tokens": 36, "total_tokens": 26188},
	  "total_cost_usd": 0.0074596,
	  "modelUsage": {"grok-4.6-build": {"inputTokens": 20392, "modelCalls": 1}}
	}`
	text, tokens, model := parseGrokOutput(envelope)
	if text != "done: added the tests" {
		t.Errorf("text = %q, want the agent's prose", text)
	}
	if tokens != 26188 {
		t.Errorf("tokens = %d, want 26188 (the CLI's own count)", tokens)
	}
	if model != "grok-4.6-build" {
		t.Errorf("model = %q, want the id from modelUsage", model)
	}
}

func TestParseGrokOutputPicksBusiestModel(t *testing.T) {
	const envelope = `{"text":"x","usage":{"total_tokens":5},
	  "modelUsage":{"small":{"modelCalls":1},"big":{"modelCalls":7}}}`
	_, _, model := parseGrokOutput(envelope)
	if model != "big" {
		t.Errorf("model = %q, want the model that did the most calls", model)
	}
}

// Non-JSON output must degrade to prose rather than blow up, so an older or
// changed CLI still produces a usable Result.
func TestParseGrokOutputFallsBackToProse(t *testing.T) {
	const prose = "I edited the file.\n```aoa:usage\n{\"tokens\": 42, \"model\": \"m\"}\n```\n"
	text, tokens, model := parseGrokOutput(prose)
	if text != prose {
		t.Errorf("non-JSON output should pass through verbatim, got %q", text)
	}
	if tokens != 42 || model != "m" {
		t.Errorf("fence fallback = (%d, %q), want (42, \"m\")", tokens, model)
	}
	// And with neither JSON nor a fence: zero, honestly.
	if _, tk, _ := parseGrokOutput("just prose"); tk != 0 {
		t.Errorf("unknown usage = %d, want 0 (never invented)", tk)
	}
}

// Subtasks must still be found when the prose is wrapped in the JSON envelope.
func TestParseGrokOutputFindsSubtasksInsideEnvelope(t *testing.T) {
	env := `{"text":"splitting this up\n` + "```aoa:subtasks\\n" +
		`[{\"local_id\":\"a\",\"title\":\"first\",\"depends_on\":[]}]` + "\\n```" +
		`\n","usage":{"total_tokens":1},"modelUsage":{"m":{"modelCalls":1}}}`
	text, _, _ := parseGrokOutput(env)
	subs := parseSubtasks(text)
	if len(subs) != 1 || subs[0].Title != "first" {
		t.Fatalf("subtasks = %+v, want one titled \"first\"", subs)
	}
}
