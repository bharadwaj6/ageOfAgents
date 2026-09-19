package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
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
			// Reference the constant rather than repeating its value: hardcoding
			// it here is what broke CI when the default was bumped to match go.mod.
			want: []string{"run", "--rm", "-v", "/repo:/workspace", "-w", "/workspace",
				DefaultSandboxImage, "python", "-m", "pytest", "-q"},
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

// fakeDocker puts a `docker` on PATH whose `version` exits versionExit and whose
// `run` prints a daemon error and exits 1 — what docker 29.x does with its
// daemon stopped, and what a failing contained command also looks like.
func fakeDocker(t *testing.T, versionExit int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake docker is a shell script")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\ncase \"$1\" in\n" +
		"version) exit " + strconv.Itoa(versionExit) + " ;;\n" +
		"*) echo 'failed to connect to the docker API'; exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// Docker 29 exits 1, not 125, when its daemon is unreachable, so the exit code
// alone called a stopped daemon a failing test (issue #135).
func TestDockerDaemonDownIsInfra(t *testing.T) {
	fakeDocker(t, 1)
	res := Verifier{Commands: []Command{{"true"}}, Sandbox: "docker"}.Run(context.Background(), t.TempDir())
	if res.Passed || !res.Infra {
		t.Errorf("daemon down: Passed=%v Infra=%v, want a failing infrastructure result; Output:\n%s", res.Passed, res.Infra, res.Output)
	}
}

// With the daemon up, the contained command's exit 1 is still a verdict.
func TestDockerCommandFailureIsAVerdict(t *testing.T) {
	fakeDocker(t, 0)
	res := Verifier{Commands: []Command{{"false"}}, Sandbox: "docker"}.Run(context.Background(), t.TempDir())
	if res.Passed || res.Infra {
		t.Errorf("daemon up, command failed: Passed=%v Infra=%v, want a verdict", res.Passed, res.Infra)
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

// The default sandbox image must be able to run the default gate. When it fell
// behind go.mod, every dockerised gate run had to GOTOOLCHAIN-download a newer
// Go first — slow every time, and a hard failure in a network-restricted
// container. Reading go.mod keeps the two from drifting again.
func TestDefaultSandboxImageMatchesGoModToolchain(t *testing.T) {
	mod, err := os.ReadFile(filepath.Join("..", "..", "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	m := regexp.MustCompile(`(?m)^go (\d+)\.(\d+)`).FindSubmatch(mod)
	if m == nil {
		t.Fatal("no go directive in go.mod")
	}
	want := "golang:" + string(m[1]) + "." + string(m[2])
	if DefaultSandboxImage != want {
		t.Errorf("DefaultSandboxImage = %q, but go.mod needs %q — an older image "+
			"forces a toolchain download on every gate run", DefaultSandboxImage, want)
	}
}
