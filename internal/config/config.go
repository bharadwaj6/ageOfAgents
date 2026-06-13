// Package config loads and saves the single aoa.toml file that configures a
// town: the integration repo, the agent backend, the concurrency governor, the
// verification gate, and the conventions injected into agent prompts.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// FileName is the per-town config file.
const FileName = "aoa.toml"

// Config is the on-disk town configuration.
type Config struct {
	// Repo is the path to the integration git repository, relative to the town
	// root (the directory containing aoa.toml) or absolute.
	Repo string `toml:"repo"`
	// Backend selects the agent backend: "mock" or "claudecode".
	Backend string `toml:"backend"`
	// Concurrency caps the number of workers in flight (the governor).
	Concurrency int `toml:"concurrency"`
	// MaxAttempts is how many times a ticket is retried before failing.
	MaxAttempts int `toml:"max_attempts"`
	// ConventionsFile, if set, is read and injected into every agent prompt.
	ConventionsFile string `toml:"conventions_file"`
	// Verify is the ordered objective gate: each entry is an argv.
	Verify [][]string `toml:"verify"`
}

// Default returns a config with sensible defaults: an offline mock backend and
// a Go build+test gate.
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
