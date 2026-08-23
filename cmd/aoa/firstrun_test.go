package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/config"
)

// A mistyped --path used to mint the directory and report "no goals submitted",
// because ledger.Open MkdirAll's its parent unconditionally.
func TestOpenWorkspaceRejectsANonWorkspace(t *testing.T) {
	tmp := t.TempDir()
	typo := filepath.Join(tmp, "typoo")

	_, err := openWorkspace(typo)
	if err == nil {
		t.Fatal("openWorkspace accepted a directory that is not a workspace")
	}
	if !strings.Contains(err.Error(), "aoa init") {
		t.Errorf("error should point at `aoa init`, got %q", err)
	}
	if _, statErr := os.Stat(typo); statErr == nil {
		t.Error("a rejected path must not be created on disk")
	}

	// With a config present it resolves.
	if err := os.WriteFile(filepath.Join(tmp, config.FileName), []byte("backend = \"mock\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := openWorkspace(tmp); err != nil {
		t.Errorf("a real workspace should resolve, got %v", err)
	}
}

// A CLI-driven backend whose binary is missing must fail at startup rather than
// dispatch, fail with an exec error, and burn the whole retry budget first.
func TestBuildBackendPreflightsTheCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no `grok`, no `claude`

	for _, name := range []string{"grok", "claudecode"} {
		_, err := buildBackendSingle(name, config.Config{})
		if err == nil {
			t.Errorf("backend %q built with no CLI on PATH", name)
			continue
		}
		if !strings.Contains(err.Error(), "PATH") {
			t.Errorf("backend %q: error should mention PATH, got %q", name, err)
		}
	}

	// mock needs nothing and must keep working offline.
	if _, err := buildBackendSingle("mock", config.Config{}); err != nil {
		t.Errorf("mock backend should never need a CLI, got %v", err)
	}
}

// Go's flag package stops at the first non-flag argument, so a flag written
// after the positional text was silently absorbed into it.
func TestRejectStrayFlags(t *testing.T) {
	if err := rejectStrayFlags([]string{"fix", "the", "parser"}); err != nil {
		t.Errorf("plain text should be accepted, got %v", err)
	}
	err := rejectStrayFlags([]string{"fix", "the", "parser", "--path", "./ws"})
	if err == nil {
		t.Fatal("a trailing --path must be rejected, not swallowed into the text")
	}
	if !strings.Contains(err.Error(), "--path") {
		t.Errorf("error should name the offending flag, got %q", err)
	}
}

// Both documented forms must honour --count. Go's flag package stops at the
// first non-flag argument, so a single Parse can only ever handle one of them —
// and before parseWithSubcommand both silently dropped --count.
func TestEventsFlagsWorkOnEitherSideOfTheSubcommand(t *testing.T) {
	for _, args := range [][]string{
		{"tail", "--count", "3", "--path", "/ws"},
		{"--path", "/ws", "tail", "--count", "3"},
		{"--path", "/ws", "--count", "3", "tail"},
	} {
		fs := flag.NewFlagSet("events", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		path := fs.String("path", ".", "")
		count := fs.Int("count", 20, "")

		sub, err := parseWithSubcommand(fs, args, "tail")
		if err != nil {
			t.Errorf("%v: %v", args, err)
			continue
		}
		if sub != "tail" || *count != 3 || *path != "/ws" {
			t.Errorf("%v -> sub=%q count=%d path=%q, want tail/3//ws", args, sub, *count, *path)
		}
	}

	// No subcommand at all falls back to the default.
	fs := flag.NewFlagSet("events", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	count := fs.Int("count", 20, "")
	sub, err := parseWithSubcommand(fs, []string{"--count", "7"}, "tail")
	if err != nil || sub != "tail" || *count != 7 {
		t.Errorf("bare flags -> sub=%q count=%d err=%v, want tail/7/nil", sub, *count, err)
	}
}
