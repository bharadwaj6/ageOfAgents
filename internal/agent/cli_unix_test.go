//go:build !windows

package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

// An agent that exits cleanly on SIGTERM must still read as cancelled, never as
// a success: a success would hand its half-finished worktree to the Gate (#217).
func TestDefaultRunnerReportsCancellationOfAGracefulAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)

	start := time.Now()
	_, _, err := defaultRunner(ctx, t.TempDir(), "sh", "-c", `trap 'exit 0' TERM; sleep 30 & wait`)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want it to wrap context.Canceled", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("defaultRunner took %v to return after cancel", d)
	}
}
