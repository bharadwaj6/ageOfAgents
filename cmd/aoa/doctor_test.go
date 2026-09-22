package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/config"
)

// find returns the check with the given name, or fails the test.
func find(t *testing.T, checks []check, name string) check {
	t.Helper()
	for _, c := range checks {
		if c.name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %v", name, names(checks))
	return check{}
}

func names(checks []check) []string {
	out := make([]string, len(checks))
	for i, c := range checks {
		out[i] = c.name
	}
	return out
}

// writeWorkspace lays down the minimum a workspace needs: an aoa.toml and a git
// repo for the Gate to run against.
func writeWorkspace(t *testing.T, toml string) string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, repo)
	if err := os.WriteFile(filepath.Join(root, "aoa.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestDoctorPassesOnAHealthyWorkspace(t *testing.T) {
	root := writeWorkspace(t, `
repo    = "./repo"
backend = "mock"
verify  = [["go", "build", "./..."]]
`)
	checks := runDoctor(root)

	for _, c := range checks {
		if !c.ok && !c.warn {
			t.Errorf("check %q failed on a healthy workspace: %s", c.name, c.detail)
		}
	}
	if got := find(t, checks, "event log"); !got.ok {
		t.Errorf("an absent Event Log is a fresh workspace, not a failure: %s", got.detail)
	}
}

// The whole point of doctor: say what is wrong *and* how to fix it. A check that
// fails without a fix has moved the work rather than doing it.
func TestDoctorFailuresCarryAFix(t *testing.T) {
	root := writeWorkspace(t, `
repo    = "./repo"
backend = "openai"
verify  = [["definitely-not-a-real-binary", "build"]]
`)
	t.Setenv("OPENAI_API_KEY", "")

	checks := runDoctor(root)

	gate := find(t, checks, "gate:definitely-not-a-real-binary")
	if gate.ok {
		t.Fatal("a Gate command whose binary is missing must fail — nothing merges without it")
	}
	if !strings.Contains(gate.fix, "aoa.toml") {
		t.Errorf("gate fix should mention aoa.toml, got %q", gate.fix)
	}

	be := find(t, checks, "backend:openai")
	if be.ok {
		t.Fatal("a backend with no API key must fail at doctor, not after the retry budget")
	}
	if !strings.Contains(be.fix, "OPENAI_API_KEY") {
		t.Errorf("backend fix should name the env var, got %q", be.fix)
	}

	for _, c := range checks {
		if !c.ok && c.fix == "" {
			t.Errorf("check %q failed with no fix line", c.name)
		}
	}
}

func TestDoctorRejectsANonWorkspace(t *testing.T) {
	checks := runDoctor(filepath.Join(t.TempDir(), "nope"))

	ws := find(t, checks, "workspace")
	if ws.ok {
		t.Fatal("a directory with no aoa.toml is not a workspace")
	}
	if !strings.Contains(ws.fix, "aoa init") {
		t.Errorf("fix should point at `aoa init`, got %q", ws.fix)
	}
}

// An empty Gate means every proposal merges unverified. That is worth saying out
// loud, but it is a choice the user can legitimately make, so it warns.
func TestDoctorWarnsOnAnEmptyGate(t *testing.T) {
	root := writeWorkspace(t, `
repo    = "./repo"
backend = "mock"
verify  = []
`)
	got := find(t, runDoctor(root), "gate")
	if !got.warn {
		t.Errorf("an empty Gate should warn, got ok=%v warn=%v", got.ok, got.warn)
	}
	if !strings.Contains(got.detail, "unverified") {
		t.Errorf("the warning should say what it costs, got %q", got.detail)
	}
}

// An override that still runs the preset's binary emits the same output
// envelope and so reports token usage. A non-preset CLI backend (or a wrapper)
// reports usage if it prints a recognized envelope or fence; doctor explains
// how its usage is read rather than claiming it reports none.
func TestDoctorTrustsAnOverriddenPresetForUsage(t *testing.T) {
	dir := t.TempDir()
	for _, bin := range []string{"claude", "my-claude-wrapper", "agy"} {
		if err := os.WriteFile(filepath.Join(dir, bin), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)

	tests := []struct {
		name    string
		backend string
		bin     string
		wantBYO bool
	}{
		{name: "override keeps the preset's binary", backend: "claudecode", bin: "claude"},
		{name: "override runs a wrapper", backend: "claudecode", bin: "my-claude-wrapper", wantBYO: true},
		{name: "byo harness that happens to run claude", backend: "mycoder", bin: "claude", wantBYO: true},
		{name: "byo harness like agy", backend: "agy", bin: "agy", wantBYO: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{
				Backend:  tt.backend,
				Backends: map[string]config.BackendConfig{tt.backend: {Type: "cli", Bin: tt.bin}},
			}
			c := checkOneBackend(tt.backend, cfg)
			if !c.ok {
				t.Fatalf("backend should be healthy, got %q", c.detail)
			}
			claimsBYO := strings.Contains(c.detail, byoCLIUsageDetail)
			if claimsBYO != tt.wantBYO {
				t.Errorf("claims byo usage = %v, want %v; detail: %q", claimsBYO, tt.wantBYO, c.detail)
			}
			if tt.wantBYO {
				if strings.Contains(c.detail, "reports no token usage") {
					t.Errorf("should not claim no token usage; detail: %q", c.detail)
				}
				if !strings.Contains(c.detail, "aoa status after a first run shows whether it was charged") {
					t.Errorf("should advise checking aoa status; detail: %q", c.detail)
				}
			}
		})
	}
}

// TestDoctorSaysTheAgentIsNotConfined pins the honest answer to "what can the
// agent reach?" — issue #101. A real backend executes commands the model chose
// as the user running aoa, and `sandbox` never changes that, so doctor has to
// say so before the first real run rather than leaving the user to find out.
func TestDoctorSaysTheAgentIsNotConfined(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		wantOK  bool
		wantAny []string
	}{
		{
			name: "mock backend runs nothing a model chose",
			toml: `
repo    = "./repo"
backend = "mock"
verify  = [["go", "build", "./..."]]
`,
			wantOK:  true,
			wantAny: []string{"mock"},
		},
		{
			name: "a real backend is unconfined",
			toml: `
repo    = "./repo"
backend = "claudecode"
verify  = [["go", "build", "./..."]]
`,
			wantAny: []string{"claudecode", "credentials"},
		},
		{
			name: "a real fallback is unconfined too",
			toml: `
repo              = "./repo"
backend           = "mock"
fallback_backends = ["codex"]
verify            = [["go", "build", "./..."]]
`,
			wantAny: []string{"codex"},
		},
		{
			name: "the docker sandbox is named as covering the Gate only",
			toml: `
repo    = "./repo"
backend = "claudecode"
sandbox = "docker"
verify  = [["go", "build", "./..."]]
`,
			wantAny: []string{"Gate"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := find(t, runDoctor(writeWorkspace(t, tc.toml)), "confinement")

			if tc.wantOK {
				if !got.ok {
					t.Errorf("want an ok check, got ok=%v warn=%v: %s", got.ok, got.warn, got.detail)
				}
			} else if got.ok || !got.warn {
				// A warning, never a failure: being unconfined is the
				// documented design, so it must not break `aoa doctor` in CI.
				t.Errorf("want a warning, got ok=%v warn=%v: %s", got.ok, got.warn, got.detail)
			}

			text := got.detail + " " + got.fix
			for _, want := range tc.wantAny {
				if !strings.Contains(text, want) {
					t.Errorf("confinement check never mentions %q: %s", want, text)
				}
			}
		})
	}
}

// gitInit makes a real repository: checkRepo shells out to git, so a bare .git
// directory would not do. Identity is passed per-command so the test does not
// depend on the developer's global git config.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
}

func TestDoctorFlagsUnreadableConventionsFile(t *testing.T) {
	root := writeWorkspace(t, `
repo             = "./repo"
backend          = "mock"
conventions_file = "DOES_NOT_EXIST.md"
verify           = [["go", "build", "./..."]]
`)
	checks := runDoctor(root)
	c := find(t, checks, "conventions")
	if c.ok {
		t.Fatal("doctor must flag a missing conventions_file as failed")
	}
	missing := filepath.Join(root, "DOES_NOT_EXIST.md")
	if !strings.Contains(c.detail, missing) {
		t.Errorf("doctor detail must name resolved path %q, got: %q", missing, c.detail)
	}
	if c.fix == "" {
		t.Fatal("conventions failure must carry a fix line")
	}

	// An existing readable conventions file passes.
	if err := os.WriteFile(missing, []byte("# Rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	checks = runDoctor(root)
	c = find(t, checks, "conventions")
	if !c.ok {
		t.Fatalf("doctor must pass when conventions_file exists and is readable, got: %q", c.detail)
	}

	// An unset conventions_file stays fine.
	rootUnset := writeWorkspace(t, `
repo    = "./repo"
backend = "mock"
verify  = [["go", "build", "./..."]]
`)
	checksUnset := runDoctor(rootUnset)
	cUnset := find(t, checksUnset, "conventions")
	if !cUnset.ok {
		t.Fatalf("doctor must pass when conventions_file is unset, got: %q", cUnset.detail)
	}
}
