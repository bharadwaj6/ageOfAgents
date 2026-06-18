package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicName(t *testing.T) {
	if got := NewAnthropic().Name(); got != "anthropic" {
		t.Errorf("Name() = %q, want anthropic", got)
	}
}

func TestAnthropicRequiresAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := NewAnthropic().Run(context.Background(), Task{TicketID: "t1", Title: "x", Worktree: t.TempDir()}); err == nil {
		t.Fatal("expected error when ANTHROPIC_API_KEY is unset")
	}
}

// TestAnthropicFinishLoop drives one full round-trip against a stub server: the
// model immediately calls finish, and Run returns its summary and token count.
// Hermetic — no real network, no API key.
func TestAnthropicFinishLoop(t *testing.T) {
	var gotKey, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{
			"content": [
				{"type": "text", "text": "Done."},
				{"type": "tool_use", "id": "tu_1", "name": "finish", "input": {"summary": "fixed the bug"}}
			],
			"stop_reason": "tool_use",
			"usage": {"input_tokens": 10, "output_tokens": 5}
		}`))
	}))
	defer srv.Close()

	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	a := NewAnthropic()
	a.BaseURL = srv.URL

	res, err := a.Run(context.Background(), Task{TicketID: "t1", Title: "fix it", Worktree: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Summary != "fixed the bug" {
		t.Errorf("Summary = %q, want %q", res.Summary, "fixed the bug")
	}
	if res.Tokens != 15 {
		t.Errorf("Tokens = %d, want 15", res.Tokens)
	}
	if res.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q, want claude-opus-4-8", res.Model)
	}
	if !strings.Contains(res.Trace, "Done.") {
		t.Errorf("Trace missing assistant text: %q", res.Trace)
	}
	if gotKey != "test-key" {
		t.Errorf("x-api-key header = %q, want test-key", gotKey)
	}
	if gotVersion != "2023-06-01" {
		t.Errorf("anthropic-version header = %q, want 2023-06-01", gotVersion)
	}
}
