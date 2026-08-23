package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// openai.go was the one file in this package with no tests at all, which is also
// where its 15-iteration tool loop, token accounting and "no tools called" nudge
// live. These drive it against a stub server — hermetic, no network, no API key.

func TestOpenAIName(t *testing.T) {
	if got := NewOpenAI().Name(); got != "openai" {
		t.Errorf("Name() = %q, want openai", got)
	}
	if got := NewOpenAICompatible("groq", "llama", "https://x/y", "GROQ_KEY").Name(); got != "groq" {
		t.Errorf("plugin Name() = %q, want groq", got)
	}
}

func TestOpenAIRequiresAPIKey(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	_, err := NewOpenAI().Run(context.Background(), Task{TicketID: "t1", Title: "x", Worktree: t.TempDir()})
	if err == nil {
		t.Fatal("expected an error when OPENAI_API_KEY is unset")
	}
}

// One full round-trip: the model calls finish immediately, and Run reports its
// summary and the server's token count.
func TestOpenAIFinishLoop(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices": [{"message": {"role": "assistant", "content": "Done.",
			  "tool_calls": [{"id": "tc_1", "type": "function",
			    "function": {"name": "finish", "arguments": "{\"summary\":\"fixed the bug\"}"}}]}}],
			"usage": {"total_tokens": 42}
		}`))
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "test-key")
	o := NewOpenAICompatible("openai", "gpt-test", srv.URL, "OPENAI_API_KEY")

	res, err := o.Run(context.Background(), Task{TicketID: "t1", Title: "fix it", Worktree: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Summary != "fixed the bug" {
		t.Errorf("Summary = %q, want the finish tool's summary", res.Summary)
	}
	if res.Tokens != 42 {
		t.Errorf("Tokens = %d, want 42 from the server's usage block", res.Tokens)
	}
	if res.Model != "gpt-test" {
		t.Errorf("Model = %q, want the configured model", res.Model)
	}
	if gotAuth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want a bearer token", gotAuth)
	}
}

// Tokens accumulate across iterations rather than only counting the last call —
// the same undercount that made `aoa status` report half the real spend.
func TestOpenAIAccumulatesTokensAcrossIterations(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "application/json")
		if atomic.AddInt32(&n, 1) == 1 {
			// First turn: run a harmless command, don't finish yet.
			_, _ = w.Write([]byte(`{
				"choices": [{"message": {"role": "assistant",
				  "tool_calls": [{"id": "tc_1", "type": "function",
				    "function": {"name": "bash", "arguments": "{\"command\":\"true\"}"}}]}}],
				"usage": {"total_tokens": 100}
			}`))
			return
		}
		_, _ = w.Write([]byte(`{
			"choices": [{"message": {"role": "assistant",
			  "tool_calls": [{"id": "tc_2", "type": "function",
			    "function": {"name": "finish", "arguments": "{\"summary\":\"done\"}"}}]}}],
			"usage": {"total_tokens": 25}
		}`))
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "k")
	o := NewOpenAICompatible("openai", "m", srv.URL, "OPENAI_API_KEY")

	res, err := o.Run(context.Background(), Task{TicketID: "t1", Title: "x", Worktree: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Tokens != 125 {
		t.Errorf("Tokens = %d, want 125 (100 + 25 across both turns)", res.Tokens)
	}
}

// A non-200 must surface as an error rather than a silently empty Result the
// orchestrator would misread as "agent produced no changes".
func TestOpenAISurfacesAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "k")
	o := NewOpenAICompatible("openai", "m", srv.URL, "OPENAI_API_KEY")

	if _, err := o.Run(context.Background(), Task{TicketID: "t1", Title: "x", Worktree: t.TempDir()}); err == nil {
		t.Fatal("a 429 must be returned as an error")
	}
}

// The request must carry the model and both tools, or the loop can never finish.
func TestOpenAISendsModelAndTools(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant",
		  "tool_calls":[{"id":"tc","type":"function",
		    "function":{"name":"finish","arguments":"{\"summary\":\"s\"}"}}]}}],
		  "usage":{"total_tokens":1}}`))
	}))
	defer srv.Close()

	t.Setenv("OPENAI_API_KEY", "k")
	o := NewOpenAICompatible("openai", "my-model", srv.URL, "OPENAI_API_KEY")
	if _, err := o.Run(context.Background(), Task{TicketID: "t1", Title: "x", Worktree: t.TempDir()}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if body["model"] != "my-model" {
		t.Errorf("request model = %v, want my-model", body["model"])
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("request carried %d tools, want bash and finish", len(tools))
	}
}
