//go:build windows

// Package agent provides the Backend interface and its implementations.
package agent

import (
	"bytes"
	"context"
	"os/exec"
)

// defaultRunner captures stdout and stderr apart, so the envelope on stdout is
// parsed without whatever the harness warned about on stderr.
//
// On Windows, process groups are not POSIX-compatible; the agent is started
// without Setpgid and the context cancellation uses exec.CommandContext's
// default behaviour (os.Process.Kill on the direct child). Child processes
// started by the agent are not killed automatically.
func defaultRunner(ctx context.Context, dir, name string, args ...string) (string, string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}
