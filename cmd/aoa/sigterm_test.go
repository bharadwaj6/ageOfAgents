//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSIGTERMStopsAgentAndItsChildren verifies that a SIGTERM sent to
// `aoa run` stops both the running agent process and its child processes
// instead of orphaning them (#217).
//
// The workspace uses a cli backend whose script:
//   - starts a background sleep child,
//   - writes its own pid and the child's pid to files,
//   - then sleeps indefinitely waiting to be killed.
//
// The test waits for the pid files, sends SIGTERM to aoa, and asserts that
// both processes are gone within a few seconds.
func TestSIGTERMStopsAgentAndItsChildren(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	tmp := t.TempDir()

	// Set up the workspace.
	if err := cmdInit([]string{"--path", tmp, "--repo", "./demo"}); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Write an agent script that:
	//  1. starts a background sleep (the "child"),
	//  2. writes its own pid and the child's pid to files,
	//  3. sleeps until killed.
	agentPidFile := filepath.Join(tmp, "agent.pid")
	childPidFile := filepath.Join(tmp, "child.pid")
	agentScript := filepath.Join(tmp, "agent.sh")
	scriptContent := "#!/usr/bin/env bash\n" +
		"sleep 9999 &\n" +
		"child=$!\n" +
		"echo $$ > " + agentPidFile + "\n" +
		"echo $child > " + childPidFile + "\n" +
		"sleep 9999\n"
	if err := os.WriteFile(agentScript, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("write agent script: %v", err)
	}

	// Configure the workspace to use the script as a cli backend.
	cfg := "repo = \"./demo\"\n" +
		"concurrency = 1\n" +
		"verify = [[\"true\"]]\n" +
		"[backends.mock]\n" +
		"type = \"cli\"\n" +
		"bin = \"" + agentScript + "\"\n"
	if err := os.WriteFile(filepath.Join(tmp, "aoa.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Submit a goal so the run dispatches the agent.
	if err := cmdGoal([]string{"--path", tmp, "test signal propagation"}); err != nil {
		t.Fatalf("goal: %v", err)
	}

	// Run aoa in a subprocess so we can send it a signal. Use the test binary
	// itself with AOA_TEST_CLI=1 as the aoa CLI (same pattern as frontdoor_test).
	aoaCmd := exec.Command(exe, "run", "--path", tmp)
	aoaCmd.Env = append(os.Environ(), "AOA_TEST_CLI=1")
	if err := aoaCmd.Start(); err != nil {
		t.Fatalf("start aoa: %v", err)
	}

	// Wait for both pid files to appear — agent script is running.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err1 := os.Stat(agentPidFile); err1 == nil {
			if _, err2 := os.Stat(childPidFile); err2 == nil {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := os.Stat(agentPidFile); err != nil {
		killForCleanup(t, aoaCmd.Process.Pid)
		t.Fatal("timed out waiting for agent pid file")
	}
	if _, err := os.Stat(childPidFile); err != nil {
		killForCleanup(t, aoaCmd.Process.Pid)
		t.Fatal("timed out waiting for child pid file")
	}

	agentPID := readPid(t, agentPidFile)
	childPID := readPid(t, childPidFile)

	// Send SIGTERM to aoa.
	if err := aoaCmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal aoa: %v", err)
	}

	// aoa should exit cleanly.
	done := make(chan error, 1)
	go func() { done <- aoaCmd.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		killForCleanup(t, aoaCmd.Process.Pid)
		t.Fatal("aoa did not exit after SIGTERM")
	}

	// Both the agent and its child must be gone.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(agentPID) && !processExists(childPID) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if processExists(agentPID) {
		killForCleanup(t, agentPID)
		t.Errorf("agent process %d is still running after aoa received SIGTERM", agentPID)
	}
	if processExists(childPID) {
		killForCleanup(t, childPID)
		t.Errorf("agent child process %d is still running after aoa received SIGTERM", childPID)
	}
}

// readPid reads a pid from a file written by the agent script.
func readPid(t *testing.T, file string) int {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read pid file %s: %v", file, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatalf("parse pid from %s: %v", file, err)
	}
	return pid
}

// processExists reports whether a process with the given pid is currently
// running. It uses kill(pid, 0), which succeeds iff the process exists and
// we have permission to signal it.
func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// killForCleanup stops a process a failed assertion left running, so it does
// not outlive the test; a process that is already gone is not an error.
func killForCleanup(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Logf("cleanup: kill %d: %v", pid, err)
	}
}
