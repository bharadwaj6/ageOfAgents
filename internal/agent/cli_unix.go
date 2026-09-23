//go:build !windows

package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// agentWaitDelay is how long exec.CommandContext waits after sending SIGTERM to
// the agent's process group before it escalates to SIGKILL. A few seconds is
// enough for any agent CLI to notice the signal and flush its state; the hard
// kill is the backstop that prevents a stuck agent from holding the worktree
// forever.
const agentWaitDelay = 5 * time.Second

// defaultRunner starts the agent in its own process group so that a context
// cancellation signals the whole group — the agent CLI and every subprocess it
// spawned (shells, test runners, compilers). Without Setpgid the group is
// inherited from aoa's own process, and only the direct child's pid is
// cancelled by exec.CommandContext (#217).
//
// On cancellation cmd.Cancel sends SIGTERM to the process group (negative pgid
// = all processes in the group), then cmd.WaitDelay escalates to SIGKILL if the
// group has not exited within agentWaitDelay.
func defaultRunner(ctx context.Context, dir, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = agentWaitDelay

	// Cancel sends SIGTERM to the entire process group, not just the direct
	// child. exec.CommandContext's default cancel is os.Process.Kill (SIGKILL).
	// A nil return makes Run report ctx.Err(), so a cancelled agent reads as
	// cancelled rather than as an agent that died of a signal.
	cmd.Cancel = func() error {
		pgid := cmd.Process.Pid // Setpgid makes pgid == pid of the child
		if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone // the group had already exited
			}
			return fmt.Errorf("kill process group -%d: %w", pgid, err)
		}
		return nil
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}
