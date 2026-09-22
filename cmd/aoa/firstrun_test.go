package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/config"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/stretchr/testify/require"
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

	for _, name := range agent.CLINames() {
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

// A BYOHarness backend gets the same startup check as a built-in — otherwise the
// escape hatch would be the one path that fails late.
func TestBuildBackendPreflightsAByoCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	cfg := config.Config{Backends: map[string]config.BackendConfig{
		"mycoder": {Type: "cli", Bin: "definitely-not-a-real-binary", Args: []string{"run"}},
	}}
	_, err := buildBackendSingle("mycoder", cfg)
	if err == nil {
		t.Fatal("a BYO CLI whose binary is missing must fail at startup")
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Errorf("error should mention PATH, got %q", err)
	}
}

// type = "cli" with no bin is a config mistake worth naming precisely: without
// it the backend would try to exec the empty string.
func TestByoCLIRequiresABin(t *testing.T) {
	cfg := config.Config{Backends: map[string]config.BackendConfig{
		"mycoder": {Type: "cli"},
	}}
	_, err := buildBackendSingle("mycoder", cfg)
	if err == nil {
		t.Fatal(`type = "cli" with no bin must be rejected`)
	}
	if !strings.Contains(err.Error(), "bin") {
		t.Errorf("error should name the missing key, got %q", err)
	}
}

// A [backends.<name>] block shadowing a preset is the documented way to correct
// a harness whose flags have moved, without waiting for a release.
func TestConfiguredBackendShadowsAPreset(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "codex")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	cfg := config.Config{Backends: map[string]config.BackendConfig{
		"codex": {Type: "cli", Bin: "codex", Args: []string{"exec", "--brand-new-flag"}},
	}}
	b, err := buildBackendSingle("codex", cfg)
	if err != nil {
		t.Fatalf("shadowing a preset should work: %v", err)
	}
	if b.Name() != "codex" {
		t.Errorf("name = %q, want codex — it is the [pricing] key", b.Name())
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

// Every published binary used to report nothing about which build it was —
// there was no version command and no ldflags stamping.
func TestVersionString(t *testing.T) {
	if got := versionString(); !strings.HasPrefix(got, "aoa ") {
		t.Errorf("versionString() = %q, want it to start with the binary name", got)
	}
	// Unstamped builds must say "dev", never claim a release they aren't.
	if version != "dev" {
		t.Errorf("default version = %q, want \"dev\" so an unstamped build cannot pose as a release", version)
	}

	old := version
	defer func() { version, commit, date = old, "", "" }()
	version, commit, date = "v0.2.0", "abc1234", "2026-08-24"
	want := "aoa v0.2.0 (abc1234, 2026-08-24)"
	if got := versionString(); got != want {
		t.Errorf("stamped versionString() = %q, want %q", got, want)
	}
}

// PR delivery mode fails at startup, not at the first push, when the remote
// is missing or the opener is not installed.
func TestBuildOrchestratorPreflightsPRDelivery(t *testing.T) {
	tests := []struct {
		name    string
		remote  bool
		openPR  []string
		wantErr string
	}{
		{name: "no such remote", openPR: []string{}, wantErr: `remote "origin"`},
		{name: "opener not on PATH", remote: true, openPR: []string{"aoa-no-such-opener"}, wantErr: `"aoa-no-such-opener"`},
		{name: "push only", remote: true, openPR: []string{}},
		{name: "default opener", remote: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp, ws := newMockWorkspace(t)
			cfg, err := config.Load(ws.configPath)
			require.NoError(t, err)
			if tt.remote {
				runGit(t, resolve(tmp, cfg.Repo), "remote", "add", "origin", filepath.Join(tmp, "origin.git"))
			}
			cfg.Delivery = config.DeliveryConfig{Mode: "pr", OpenPR: tt.openPR}
			require.NoError(t, cfg.Save(ws.configPath))
			led, err := ledger.Open(ws.ledgerPath)
			require.NoError(t, err)

			o, err := buildOrchestrator(ws, led, state.Budget{})
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, o)
		})
	}
}

// The governor warning is a claim about whether aoa can count tokens, and a
// wrong claim is worse than none: the dogfood workspace overrides the
// claudecode preset to add flags, and was told its ceilings were inert while
// the same run billed 163k tokens. Usage is read off the harness's output
// envelope, so an override that still runs the preset's binary still reports.
// A non-preset CLI backend (or a wrapper) gets the honest warning: usage is
// read from output if it prints a recognized envelope or fence, and aoa status
// shows whether it was charged.
func TestWarnInertGovernorsTrustsAnOverriddenPreset(t *testing.T) {
	claudecode := func(bin string) map[string]config.BackendConfig {
		return map[string]config.BackendConfig{"claudecode": {Type: "cli", Bin: bin}}
	}
	tests := []struct {
		name     string
		backend  string
		backends map[string]config.BackendConfig
		noLimits bool
		wantWarn bool
		wantBYO  bool
	}{
		{name: "bare preset that reports", backend: "claudecode"},
		{name: "override with the preset's bin", backend: "claudecode", backends: claudecode("claude")},
		{name: "override with an absolute path to it", backend: "claudecode", backends: claudecode("/opt/homebrew/bin/claude")},
		{name: "override running another binary", backend: "claudecode", backends: claudecode("my-claude-wrapper"), wantWarn: true, wantBYO: true},
		{name: "override of a preset that reports nothing", backend: "cursor",
			backends: map[string]config.BackendConfig{"cursor": {Type: "cli", Bin: "cursor-agent"}}, wantWarn: true, wantBYO: true},
		// A leftover bin from an earlier cli block is not a promise: an HTTP
		// plugin never runs it, so its envelope is not what aoa will parse.
		{name: "http plugin shadowing the name", backend: "claudecode",
			backends: map[string]config.BackendConfig{"claudecode": {Type: "openai_compatible", Model: "x", Bin: "claude"}}, wantWarn: true},
		{name: "byo harness", backend: "mycoder",
			backends: map[string]config.BackendConfig{"mycoder": {Type: "cli", Bin: "mycoder"}}, wantWarn: true, wantBYO: true},
		{name: "byo harness like agy", backend: "agy",
			backends: map[string]config.BackendConfig{"agy": {Type: "cli", Bin: "agy"}}, wantWarn: true, wantBYO: true},
		{name: "no ceilings, nothing to warn about", backend: "mycoder",
			backends: map[string]config.BackendConfig{"mycoder": {Type: "cli", Bin: "mycoder"}}, noLimits: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{Backend: tt.backend, Backends: tt.backends}
			if !tt.noLimits {
				cfg.MaxTokensPerGoal = 1000
			}
			var buf strings.Builder
			warnInertGovernors(cfg, &buf)
			warned := buf.Len() > 0
			if warned != tt.wantWarn {
				t.Errorf("warned = %v, want %v; output: %q", warned, tt.wantWarn, buf.String())
			}
			if tt.wantBYO {
				if !strings.Contains(buf.String(), byoCLIUsageDetail) {
					t.Errorf("expected BYO usage detail in warning; got %q", buf.String())
				}
				if strings.Contains(buf.String(), "does not report token usage") {
					t.Errorf("should not claim no token usage for BYO CLI backend; got %q", buf.String())
				}
			} else if tt.wantWarn {
				if !strings.Contains(buf.String(), "does not report token usage") {
					t.Errorf("expected inert governor warning; got %q", buf.String())
				}
			}
		})
	}
}
