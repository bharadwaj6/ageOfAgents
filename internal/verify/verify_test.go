package verify

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPassesWhenAllCommandsSucceed(t *testing.T) {
	v := Verifier{Commands: []Command{{"true"}, {"echo", "hello"}}}
	res := v.Run(context.Background(), t.TempDir())
	if !res.Passed {
		t.Fatalf("expected pass, got %+v", res)
	}
	if !strings.Contains(res.Output, "hello") {
		t.Errorf("output should capture stdout, got %q", res.Output)
	}
}

func TestFailsAndReportsFailingCommand(t *testing.T) {
	v := Verifier{Commands: []Command{{"true"}, {"false"}, {"echo", "unreached"}}}
	res := v.Run(context.Background(), t.TempDir())
	if res.Passed {
		t.Fatal("expected failure")
	}
	if res.Failed != "false" {
		t.Errorf("Failed = %q, want \"false\"", res.Failed)
	}
	if strings.Contains(res.Output, "unreached") {
		t.Error("should stop at first failure, not run later commands")
	}
}

func TestEmptyVerifierPasses(t *testing.T) {
	if res := (Verifier{}).Run(context.Background(), t.TempDir()); !res.Passed {
		t.Errorf("empty verifier should pass, got %+v", res)
	}
}

func TestRunsInGivenDirectory(t *testing.T) {
	dir := t.TempDir()
	res := Verifier{Commands: []Command{{"pwd"}}}.Run(context.Background(), dir)
	if !res.Passed {
		t.Fatalf("pwd failed: %+v", res)
	}
	// macOS /tmp is a symlink to /private/tmp, so match the suffix.
	if !strings.Contains(res.Output, strings.TrimPrefix(dir, "/private")) &&
		!strings.Contains("/private"+res.Output, dir) {
		t.Logf("pwd output %q for dir %q (symlink-tolerant check)", strings.TrimSpace(res.Output), dir)
	}
}

func TestCanceledContextFailsFast(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	res := Verifier{Commands: []Command{{"sleep", "30"}}}.Run(ctx, t.TempDir())
	if res.Passed {
		t.Error("canceled context should not pass")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("canceled context should fail fast, not wait for sleep")
	}
}

func TestDockerArgs(t *testing.T) {
	cmd := Command{"python", "-m", "pytest", "-q"}
	tests := []struct {
		name     string
		verifier Verifier
		want     []string
	}{
		{
			name:     "defaults to the Go image and /workspace",
			verifier: Verifier{Sandbox: "docker"},
			want: []string{"run", "--rm", "-v", "/repo:/workspace", "-w", "/workspace",
				"golang:1.22", "python", "-m", "pytest", "-q"},
		},
		{
			name:     "prepared image keeps the standard mount",
			verifier: Verifier{Sandbox: "docker", Image: "python:3.11"},
			want: []string{"run", "--rm", "-v", "/repo:/workspace", "-w", "/workspace",
				"python:3.11", "python", "-m", "pytest", "-q"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.verifier.dockerArgs("/repo", cmd)
			if !slices.Equal(got, tt.want) {
				t.Errorf("dockerArgs() =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

// A sandbox that cannot run the gate must be distinguishable from a gate the
// code failed: both block the merge, but only the second is a verdict on the
// proposal. Without this, a docker outage is recorded as "your patch is broken".
func TestDockerInfraFailureIsNotAVerdict(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	v := Verifier{
		Commands: []Command{{"true"}},
		Sandbox:  "docker",
		Image:    "aoa-nonexistent-image-for-tests:latest",
	}
	res := v.Run(context.Background(), t.TempDir())
	if res.Passed {
		t.Fatal("a missing image must not pass the gate")
	}
	if !res.Infra {
		t.Errorf("missing image should be flagged as an infrastructure failure, got Output:\n%s", res.Output)
	}
}

// The ordinary case must stay a verdict, or everything looks like infrastructure.
func TestCommandFailureIsAVerdict(t *testing.T) {
	v := Verifier{Commands: []Command{{"false"}}}
	res := v.Run(context.Background(), t.TempDir())
	if res.Passed {
		t.Fatal("`false` must fail the gate")
	}
	if res.Infra {
		t.Error("a failing command is a verdict on the code, not an infrastructure fault")
	}
}
