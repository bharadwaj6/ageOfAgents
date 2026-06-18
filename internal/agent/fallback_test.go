package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
)

type failingBackend struct {
	name string
	err  error
}

func (f *failingBackend) Name() string { return f.name }
func (f *failingBackend) Run(ctx context.Context, task agent.Task) (agent.Result, error) {
	return agent.Result{}, f.err
}

func TestFallbackBackend_Success(t *testing.T) {
	err1 := errors.New("rate limited")
	err2 := errors.New("internal server error")

	b1 := &failingBackend{name: "fail1", err: err1}
	b2 := &failingBackend{name: "fail2", err: err2}
	b3 := agent.NewMock() // always succeeds

	fb := agent.NewFallbackBackend(b1, b2, b3)

	// Worktree must be a temp dir: the Mock backend writes a marker file there,
	// and an empty Worktree would leak it into the package source tree.
	res, err := fb.Run(context.Background(), agent.Task{TicketID: "t-1", Title: "some title", Worktree: t.TempDir()})
	if err != nil {
		t.Fatalf("expected success from fallback, got error: %v", err)
	}
	// Mock backend always returns task.Title as Summary
	if res.Summary != "some title" {
		t.Errorf("expected mock result summary, got %q", res.Summary)
	}
}

func TestFallbackBackend_AllFail(t *testing.T) {
	err1 := errors.New("rate limited")
	err2 := errors.New("internal server error")

	b1 := &failingBackend{name: "fail1", err: err1}
	b2 := &failingBackend{name: "fail2", err: err2}

	fb := agent.NewFallbackBackend(b1, b2)

	_, err := fb.Run(context.Background(), agent.Task{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "fail1") || !strings.Contains(msg, "rate limited") {
		t.Errorf("error missing fail1 details: %v", msg)
	}
	if !strings.Contains(msg, "fail2") || !strings.Contains(msg, "internal server error") {
		t.Errorf("error missing fail2 details: %v", msg)
	}
}

func TestFallbackBackend_ContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // instantly cancel

	b1 := &failingBackend{name: "fail1", err: context.Canceled}
	b2 := agent.NewMock() // would normally succeed

	fb := agent.NewFallbackBackend(b1, b2)
	_, err := fb.Run(ctx, agent.Task{})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
