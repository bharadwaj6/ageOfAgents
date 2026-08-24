package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

// gitInit makes a real repository: checkRepo shells out to git, so a bare .git
// directory would not do. Identity is passed per-command so the test does not
// depend on the developer's global git config.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
}
