package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bharadwaj6/ageOfAgents/internal/config"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
)

// check is one diagnosis: what was inspected, whether it holds, and — when it
// does not — the single action that fixes it. The fix line is the whole point;
// a doctor that only says "FAIL: backend" has moved the work, not done it.
type check struct {
	name string
	ok   bool
	// detail explains what was found, pass or fail.
	detail string
	// fix is printed only on failure.
	fix string
	// warn downgrades a failure to a warning: something worth knowing that
	// should not make the command exit non-zero.
	warn bool
}

func cmdDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	describe(fs,
		"aoa doctor — check that this workspace can actually run.\n\n"+
			"Verifies the things that otherwise fail deep inside a run: a missing\n"+
			"backend CLI, a Gate command that isn't installed, an unreadable Event\n"+
			"Log. Exits non-zero if any check fails, so CI can gate on it.",
		"aoa doctor --path ./workspace")
	path := fs.String("path", ".", "workspace root")
	if err := fs.Parse(args); err != nil {
		return err
	}

	checks := runDoctor(*path)

	failed := 0
	for _, c := range checks {
		switch {
		case c.ok:
			fmt.Printf("  ok    %-22s %s\n", c.name, c.detail)
		case c.warn:
			fmt.Printf("  warn  %-22s %s\n", c.name, c.detail)
			if c.fix != "" {
				fmt.Printf("        %s\n", c.fix)
			}
		default:
			failed++
			fmt.Printf("  FAIL  %-22s %s\n", c.name, c.detail)
			if c.fix != "" {
				fmt.Printf("        → %s\n", c.fix)
			}
		}
	}

	fmt.Println()
	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	fmt.Println("all checks passed")
	return nil
}

// runDoctor performs every check and returns them in report order. It is a pure
// function of the filesystem so tests can call it directly on a t.TempDir().
func runDoctor(path string) []check {
	var out []check

	out = append(out, checkBinary("git", "git",
		"install git — aoa drives worktrees and merges through it"))

	ws, err := openWorkspace(path)
	if err != nil {
		return append(out, check{
			name:   "workspace",
			detail: err.Error(),
			fix:    fmt.Sprintf("aoa init --path %s --repo ./demo", path),
		})
	}
	out = append(out, check{name: "workspace", ok: true, detail: ws.root})

	cfg, err := config.Load(ws.configPath)
	if err != nil {
		return append(out, check{
			name:   "aoa.toml",
			detail: err.Error(),
			fix:    fmt.Sprintf("fix the syntax in %s", ws.configPath),
		})
	}
	out = append(out, check{name: "aoa.toml", ok: true, detail: ws.configPath})

	out = append(out, checkRepo(resolve(ws.root, cfg.Repo)))
	out = append(out, checkBackend(cfg)...)
	out = append(out, checkGate(cfg)...)
	if cfg.Sandbox == "docker" {
		out = append(out, checkBinary("docker", "docker",
			`sandbox = "docker" needs docker running — start it, or set sandbox = "" to run the Gate on the host`))
	}
	out = append(out, checkLedger(ws.ledgerPath))

	return out
}

func checkBinary(name, bin, fix string) check {
	p, err := exec.LookPath(bin)
	if err != nil {
		return check{name: name, detail: fmt.Sprintf("%q not found on $PATH", bin), fix: fix}
	}
	return check{name: name, ok: true, detail: p}
}

// checkRepo verifies the Gate has somewhere to run. A dirty tree is a warning,
// not a failure: aoa works in its own worktrees, but uncommitted changes in the
// integration repo are a good way to be surprised by what does and doesn't
// reach a Worker.
func checkRepo(repoPath string) check {
	if _, err := os.Stat(repoPath); err != nil {
		return check{
			name:   "repo",
			detail: fmt.Sprintf("%s: %v", repoPath, err),
			fix:    "set `repo` in aoa.toml to a git repository that exists",
		}
	}
	if err := exec.Command("git", "-C", repoPath, "rev-parse", "--git-dir").Run(); err != nil {
		return check{
			name:   "repo",
			detail: fmt.Sprintf("%s is not a git repository", repoPath),
			fix:    fmt.Sprintf("git -C %s init", repoPath),
		}
	}
	st, err := exec.Command("git", "-C", repoPath, "status", "--porcelain").Output()
	if err == nil && len(strings.TrimSpace(string(st))) > 0 {
		n := len(strings.Split(strings.TrimSpace(string(st)), "\n"))
		return check{
			name: "repo", warn: true,
			detail: fmt.Sprintf("%s (%d uncommitted change(s))", repoPath, n),
			fix:    "commit or stash them; Workers branch from HEAD and will not see them",
		}
	}
	return check{name: "repo", ok: true, detail: repoPath}
}

// checkBackend answers the question that otherwise costs a whole retry budget:
// is the configured backend actually usable on this machine?
func checkBackend(cfg config.Config) []check {
	names := append([]string{cfg.Backend}, cfg.FallbackBackends...)
	seen := map[string]bool{}
	var out []check
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, checkOneBackend(name, cfg))
	}
	return out
}

func checkOneBackend(name string, cfg config.Config) check {
	label := "backend:" + name

	if bCfg, ok := cfg.Backends[name]; ok {
		switch bCfg.Type {
		case "openai_compatible":
			if os.Getenv(bCfg.APIKeyEnv) == "" {
				return check{
					name:   label,
					detail: fmt.Sprintf("plugin needs $%s, which is unset", bCfg.APIKeyEnv),
					fix:    fmt.Sprintf("export %s=…", bCfg.APIKeyEnv),
				}
			}
			return check{name: label, ok: true, detail: fmt.Sprintf("plugin %q, $%s set", bCfg.Type, bCfg.APIKeyEnv)}
		default:
			return check{
				name:   label,
				detail: fmt.Sprintf("unknown plugin type %q", bCfg.Type),
				fix:    `set type = "openai_compatible" under [backends.` + name + `]`,
			}
		}
	}

	switch name {
	case "mock", "":
		return check{name: label, ok: true, detail: "offline fixture; no network, no key"}
	case "claudecode":
		return checkBinary(label, "claude", "install the claude CLI and authenticate it")
	case "grok":
		return checkBinary(label, "grok", "install the grok CLI and log in at grok.com")
	case "openai":
		return checkEnv(label, "OPENAI_API_KEY")
	case "anthropic":
		return checkEnv(label, "ANTHROPIC_API_KEY")
	default:
		return check{
			name:   label,
			detail: fmt.Sprintf("unknown backend %q", name),
			fix:    "set `backend` in aoa.toml to one of: mock, claudecode, grok, openai, anthropic",
		}
	}
}

func checkEnv(label, key string) check {
	if os.Getenv(key) == "" {
		return check{name: label, detail: fmt.Sprintf("$%s is unset", key), fix: fmt.Sprintf("export %s=…", key)}
	}
	return check{name: label, ok: true, detail: fmt.Sprintf("$%s is set", key)}
}

// checkGate resolves each Gate command's binary. Nothing merges unless the Gate
// passes, so a Gate whose first word isn't installed fails every proposal for a
// reason that looks like the agent's fault.
func checkGate(cfg config.Config) []check {
	if len(cfg.Verify) == 0 {
		return []check{{
			name:   "gate",
			warn:   true,
			detail: "no `verify` commands configured — every proposal merges unverified",
			fix:    "set `verify` in aoa.toml, e.g. verify = [[\"go\", \"build\", \"./...\"]]",
		}}
	}
	var out []check
	for _, cmd := range cfg.Verify {
		if len(cmd) == 0 {
			continue
		}
		label := "gate:" + cmd[0]
		if _, err := exec.LookPath(cmd[0]); err != nil {
			out = append(out, check{
				name:   label,
				detail: fmt.Sprintf("%q not found on $PATH (from %q)", cmd[0], strings.Join(cmd, " ")),
				fix:    fmt.Sprintf("install %s, or change `verify` in aoa.toml", cmd[0]),
			})
			continue
		}
		out = append(out, check{name: label, ok: true, detail: strings.Join(cmd, " ")})
	}
	return out
}

// checkLedger replays the Event Log. Every number aoa reports is a projection of
// this file, so a log that will not replay is the one failure that makes all the
// others unanswerable.
func checkLedger(ledgerPath string) check {
	if _, err := os.Stat(ledgerPath); err != nil {
		if os.IsNotExist(err) {
			return check{name: "event log", ok: true, detail: "empty (no run yet)"}
		}
		return check{name: "event log", detail: err.Error(), fix: "check permissions on " + filepath.Dir(ledgerPath)}
	}
	led, err := ledger.Open(ledgerPath)
	if err != nil {
		return check{name: "event log", detail: err.Error(), fix: "check permissions on " + ledgerPath}
	}
	events, err := led.Read()
	if err != nil {
		return check{
			name:   "event log",
			detail: fmt.Sprintf("cannot replay: %v", err),
			fix:    "the log is corrupt; move " + ledgerPath + " aside to start fresh",
		}
	}
	st, err := state.Fold(events)
	if err != nil {
		return check{
			name:   "event log",
			detail: fmt.Sprintf("cannot replay: %v", err),
			fix:    "the log is corrupt; move " + ledgerPath + " aside to start fresh",
		}
	}
	return check{
		name: "event log", ok: true,
		detail: fmt.Sprintf("%d event(s) replay to %d goal(s), %d ticket(s)", len(events), len(st.Goals), len(st.Tickets)),
	}
}
