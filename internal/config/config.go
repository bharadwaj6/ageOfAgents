// Package config loads and saves the single aoa.toml file that configures a
// workspace: the integration repo, the agent Backend, the Concurrency Limit, the
// Gate commands, and the Conventions injected into agent prompts.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// FileName is the per-workspace config file.
const FileName = "aoa.toml"

// Config is the on-disk workspace configuration.
type Config struct {
	// Repo is the path to the integration git repository, relative to the workspace
	// root (the directory containing aoa.toml) or absolute.
	Repo string `toml:"repo"`
	// Backend selects the agent Backend: "mock" or "claudecode".
	Backend string `toml:"backend"`
	// Concurrency caps the number of Workers in flight (the Concurrency Limit).
	Concurrency int `toml:"concurrency"`
	// MaxAttempts is how many times a Task is retried before failing.
	MaxAttempts int `toml:"max_attempts"`
	// ConventionsFile, if set, is read and injected into every agent prompt.
	ConventionsFile string `toml:"conventions_file"`
	// Verify is the ordered Gate: each entry is an argv.
	Verify [][]string `toml:"verify"`
	// RequireApproval parks every Gate-verified proposal for a human decision
	// (aoa approve / aoa reject) before it merges to main (ADR 008). Default off.
	RequireApproval bool `toml:"require_approval"`
	// MaxTokensPerGoal caps the LLM tokens a single Goal may spend before the
	// spend governor stops dispatching its remaining work (circuit breaker).
	// 0 = unlimited (default).
	MaxTokensPerGoal int `toml:"max_tokens_per_goal"`
	// RetryBackoff is the base wait before re-dispatching a failed ticket, as a
	// duration string (e.g. "2s"); the wait grows exponentially per attempt.
	// Empty or "0s" = retry immediately (default).
	RetryBackoff string `toml:"retry_backoff"`
	// CrashLoopThreshold is how many identical-reason verification failures in a
	// row make a ticket give up, even under MaxAttempts. 0 ⇒ default 3 (inert
	// while ≤ max_attempts).
	CrashLoopThreshold int `toml:"crash_loop_threshold"`
	// Pricing maps a model id (as reported by the Backend) to its cost in USD per
	// *million* tokens, used to turn token counts into a $ figure in `aoa status`.
	// Absent ⇒ unpriced ($0). Example: [pricing] then claudecode = 15.0.
	Pricing map[string]float64 `toml:"pricing"`
}

// Default returns a config with sensible defaults: an offline mock Backend and
// a Go build+test Gate.
func Default() Config {
	return Config{
		Repo:        "./repo",
		Backend:     "mock",
		Concurrency: 4,
		MaxAttempts: 2,
		Verify: [][]string{
			{"go", "build", "./..."},
			{"go", "test", "./..."},
		},
	}
}

// Load reads aoa.toml from path and fills any unset fields with defaults.
func Load(path string) (Config, error) {
	c := Config{}
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return Config{}, fmt.Errorf("load %s: %w", path, err)
	}
	return c.withDefaults(), nil
}

// Save writes the config as TOML to path.
func (c Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if err := toml.NewEncoder(f).Encode(c); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return nil
}

func (c Config) withDefaults() Config {
	d := Default()
	if c.Repo == "" {
		c.Repo = d.Repo
	}
	if c.Backend == "" {
		c.Backend = d.Backend
	}
	if c.Concurrency <= 0 {
		c.Concurrency = d.Concurrency
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = d.MaxAttempts
	}
	if c.Verify == nil {
		c.Verify = d.Verify
	}
	return c
}
