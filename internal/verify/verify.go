// Package verify runs the objective verification gate that decides whether a
// proposal may merge (docs/design/adr/002-verifier-gated-merge-queue.md). It runs a
// configured, ordered list of commands (build / tests / lint) in a directory;
// the gate passes only if every command exits zero.
package verify

import (
	"context"
	"os/exec"
	"strings"
)

// Command is an argv to execute; Command[0] is the program.
type Command []string

func (c Command) String() string { return strings.Join(c, " ") }

// Verifier is an ordered set of commands forming the gate.
type Verifier struct {
	Commands []Command
	Sandbox  string
	// Image is the container image used when Sandbox is "docker". Empty means
	// [DefaultSandboxImage], which only carries a Go toolchain — set this to a
	// prepared image when the gate needs another language's dependencies.
	Image string
}

// Defaults for the docker sandbox.
const (
	DefaultSandboxImage = "golang:1.22"
	// SandboxMount is where the repository is mounted, and the working directory,
	// inside the sandbox image. A prepared image whose dependencies were built
	// against a different path should copy the repo across as part of its gate
	// command rather than being mounted over that path, which would hide the
	// image's own build artifacts.
	SandboxMount = "/workspace"
)

// Result reports the outcome of running the gate.
type Result struct {
	Passed bool   // true iff every command exited zero
	Failed string // the first command that failed (empty if Passed)
	Output string // combined stdout+stderr across commands
}

// Run executes the commands in order within dir, stopping at the first failure.
// An empty command list passes trivially (no gate configured).
func (v Verifier) Run(ctx context.Context, dir string) Result {
	var out strings.Builder
	for _, c := range v.Commands {
		if len(c) == 0 {
			continue
		}
		var cmd *exec.Cmd
		if v.Sandbox == "docker" {
			cmd = exec.CommandContext(ctx, "docker", v.dockerArgs(dir, c)...)
		} else {
			cmd = exec.CommandContext(ctx, c[0], c[1:]...)
			cmd.Dir = dir
		}
		b, err := cmd.CombinedOutput()
		out.Write(b)
		if err != nil {
			return Result{Passed: false, Failed: c.String(), Output: out.String()}
		}
	}
	return Result{Passed: true, Output: out.String()}
}

// dockerArgs builds the `docker run` argv that executes c against the repo at
// dir, applying the Image and Mount defaults.
func (v Verifier) dockerArgs(dir string, c Command) []string {
	image := v.Image
	if image == "" {
		image = DefaultSandboxImage
	}
	args := []string{"run", "--rm", "-v", dir + ":" + SandboxMount, "-w", SandboxMount, image}
	return append(args, c...)
}

// ToCommands converts a slice of string slices into a slice of [Command].
func ToCommands(cmds [][]string) []Command {
	out := make([]Command, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, Command(c))
	}
	return out
}
