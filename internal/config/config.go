// Package config loads and saves the single aoa.toml file that configures a
// workspace: the integration repo, the agent Backend, the Concurrency Limit, the
// Gate commands, and the Conventions injected into agent prompts.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/BurntSushi/toml"
)

// FileName is the per-workspace config file.
const FileName = "aoa.toml"

// BackendConfig holds settings for a custom backend plugin.
type BackendConfig struct {
	// Type selects the plugin kind: "openai_compatible" for an HTTP endpoint
	// that speaks the OpenAI chat API, or "cli" for any harness that edits files
	// in place when invoked with a prompt.
	Type      string `toml:"type"`
	BaseURL   string `toml:"base_url"`
	Model     string `toml:"model"`
	APIKeyEnv string `toml:"api_key_env"`
	// Bin is the binary to invoke, for type = "cli".
	Bin string `toml:"bin"`
	// Args are passed to Bin verbatim; the task prompt is appended as the final
	// argument, as a single argv element (no shell). A harness that wants the
	// prompt somewhere else is a three-line wrapper script on your PATH.
	Args []string `toml:"args"`
}

// DeliveryConfig is the [delivery] table: where a verified change goes (ADR
// 016). Mode "local" (the default) merges onto the adopted repository's
// checked-out branch and pushes nothing. Mode "pr" merges each Goal's work onto
// its own branch, aoa/<goal-id>, cut from Remote's Base; once every task of
// the Goal is complete it pushes that branch to Remote (never with force) and
// runs OpenPR to open a pull request against Base.
type DeliveryConfig struct {
	Mode   string `toml:"mode"`
	Remote string `toml:"remote,omitempty"` // default "origin" in pr mode
	Base   string `toml:"base,omitempty"`   // default "main" in pr mode
	// OpenPR is the opener argv, run in the repository without a shell. The
	// placeholders {branch} {base} {title} {body} are replaced inside each
	// element, so each stays one argument. The last stdout line starting with
	// "http" is the pull request's URL. Unset in pr mode means DefaultOpenPR;
	// open_pr = [] means push only.
	OpenPR []string `toml:"open_pr"`
}

// BudgetConfig is the [budget] table: workspace-wide limits per UTC day, from
// event timestamps (ADR 017). Past a spend limit the Scheduler dispatches no
// new attempt and starts no new Goal; past the Goal limit it starts no new
// Goal. Each limit is off at 0, the default.
type BudgetConfig struct {
	USDPerDay    float64 `toml:"usd_per_day"`    // dollars, as the harnesses report them or [pricing] prices them
	TokensPerDay int     `toml:"tokens_per_day"` // tokens, every attempt's
	GoalsPerDay  int     `toml:"goals_per_day"`  // Goals started: a Goal starts when its first task is created
	// RequireRunBudget makes `aoa run` refuse to start without --max-usd or
	// --max-tokens (--max-goals alone does not count), so
	// nothing runs in this workspace unbudgeted. Default false.
	RequireRunBudget bool `toml:"require_run_budget"`
}

// DefaultOpenPR opens a pull request with the GitHub CLI, returning the one
// already open for the branch instead when there is one, so re-running it
// after a crash yields exactly one pull request.
var DefaultOpenPR = []string{
	"sh", "-c",
	`gh pr view "$1" --json url --jq .url 2>/dev/null || gh pr create --head "$1" --base "$2" --title "$3" --body "$4"`,
	"aoa-open-pr", "{branch}", "{base}", "{title}", "{body}",
}

// Config is the on-disk workspace configuration.
type Config struct {
	// Repo is the path to the integration git repository, relative to the workspace
	// root (the directory containing aoa.toml) or absolute.
	Repo string `toml:"repo"`
	// Backend selects the agent Backend. Built-in CLI harnesses: "claudecode",
	// "codex", "cursor", "gemini", "grok". Built-in HTTP backends: "openai",
	// "anthropic". "mock" is the offline fixture. Any other value must name a
	// plugin defined in Backends — including type = "cli", which drives a
	// harness aoa has no preset for.
	Backend string `toml:"backend"`
	// Concurrency caps the number of Workers in flight (the Concurrency Limit).
	Concurrency int `toml:"concurrency"`
	// MaxAttempts is how many times a Task is retried before failing.
	MaxAttempts int `toml:"max_attempts"`
	// BestOfN is the number of concurrent attempts dispatched for a task (parallel generation).
	// Default 1.
	BestOfN int `toml:"best_of_n"`
	// ConventionsFile, if set, is read and injected into every agent prompt.
	ConventionsFile string `toml:"conventions_file"`
	// Verify is the ordered Gate: each entry is an argv.
	Verify [][]string `toml:"verify"`
	// RegressionVerify is an optional broader test set run against post-merge main
	// after a proposal passes the Gate. It never blocks a merge; it measures the
	// regression-escape rate — how often the Gate's blind spot lets a regression
	// through (see docs/design/metrics.md). Empty = off.
	RegressionVerify [][]string `toml:"regression_verify"`
	// RequireApproval parks every Gate-verified proposal for a human decision
	// (aoa approve / aoa reject) before it merges to main (ADR 008). Default off.
	RequireApproval bool `toml:"require_approval"`
	// MaxTokensPerGoal caps the LLM tokens a single Goal may spend before the
	// spend governor stops dispatching its remaining work (circuit breaker).
	// 0 = unlimited (default).
	MaxTokensPerGoal int `toml:"max_tokens_per_goal"`
	// MaxUsdPerGoal caps the dollar amount a single Goal may spend.
	// Requires [pricing] to be set. 0 = unlimited (default).
	MaxUsdPerGoal float64 `toml:"max_usd_per_goal"`
	// RetryBackoff is the base wait before re-dispatching a failed ticket, as a
	// duration string (e.g. "2s"); the wait grows exponentially per attempt.
	// Empty or "0s" = retry immediately (default).
	RetryBackoff string `toml:"retry_backoff"`
	// CrashLoopThreshold is how many identical-reason verification failures in a
	// row make a ticket give up, even under MaxAttempts. 0 ⇒ default 3 (inert
	// while ≤ max_attempts).
	CrashLoopThreshold int `toml:"crash_loop_threshold"`
	// Sandbox specifies how to isolate untrusted code execution (e.g. the verifier gate).
	// Supported values: "" (no sandboxing, runs on host) or "docker".
	Sandbox string `toml:"sandbox"`
	// SandboxImage is the container image used when Sandbox is "docker". Empty
	// means verify.DefaultSandboxImage, which carries only a Go toolchain; set a
	// prepared image when the Gate needs another language's dependencies.
	SandboxImage string `toml:"sandbox_image"`

	// The termination gates below bound an unattended run. Each is zero by
	// default, which means "use the Scheduler's built-in default" — the
	// authoritative values live in orchestrator.New so they cannot drift.
	//
	// StallTimeout is how long a Worker may go without progress before the Stall
	// Detector restarts it, as a duration string (e.g. "10m"). Workers heartbeat
	// while running, so this bounds genuinely dead work, not merely slow work.
	// Empty ⇒ default 2m.
	StallTimeout string `toml:"stall_timeout"`
	// AgentTimeout is the hard ceiling on a single agent attempt, as a duration
	// string (e.g. "45m"). Distinct from StallTimeout: a running Worker
	// heartbeats, so StallTimeout bounds silence while this bounds runtime. It
	// is what stops a wedged agent CLI hanging the run indefinitely, so it wants
	// to be generous — well above how long a real task takes. Empty ⇒ default 30m.
	AgentTimeout string `toml:"agent_timeout"`
	// PollInterval is how long `aoa run` waits between reconcile passes while
	// workers are still in flight, as a duration string. Dispatch is
	// asynchronous (ADR 013) so the loop polls rather than blocking on a wave;
	// this trades responsiveness against wakeups. Empty ⇒ default 100ms.
	PollInterval string `toml:"poll_interval"`
	// MaxPasses bounds how many reconcile passes a single `aoa run` may make
	// before giving up — the backstop against a Scheduler that makes progress
	// forever without settling. 0 ⇒ default 1000.
	MaxPasses int `toml:"max_passes"`
	// MaxGraphDepth caps how deep emergent decomposition may nest (graph
	// governor, ADR 007). 0 ⇒ default 5.
	MaxGraphDepth int `toml:"max_graph_depth"`
	// MaxTicketsPerGoal caps how many Tasks one Goal may spawn in total (graph
	// governor). 0 ⇒ default 64.
	MaxTicketsPerGoal int `toml:"max_tickets_per_goal"`
	// MaxFanOut caps how many new children a single decomposition may emit
	// (graph governor). 0 ⇒ default 8.
	MaxFanOut int `toml:"max_fan_out"`
	// Pricing maps a model id (as reported by the Backend; the backend's name
	// when it reports none) to its cost in USD per *million* tokens. It is the
	// fallback: an attempt whose harness reported its own cost is charged that
	// instead. Absent ⇒ unpriced ($0). Example: [pricing] then
	// "claude-sonnet-5" = 3.0.
	Pricing map[string]float64 `toml:"pricing"`
	// Backends defines custom backend plugins: type = "openai_compatible" for an
	// OpenAI-shaped HTTP endpoint (OpenRouter, DeepSeek, a local gateway), or
	// type = "cli" to drive any coding-agent CLI. A block here shadows a
	// built-in of the same name, which is how a preset's flags get corrected
	// without waiting for a release.
	Backends map[string]BackendConfig `toml:"backends"`
	// FallbackBackends specifies an ordered list of backend IDs to try if the
	// primary Backend fails (e.g., rate limits or API errors).
	FallbackBackends []string `toml:"fallback_backends"`
	// Delivery says where verified work goes: the local branch, or a pull
	// request per Goal (ADR 016).
	Delivery DeliveryConfig `toml:"delivery"`
	// Budget holds the per-day limits and whether `aoa run` needs a run budget
	// (ADR 017). Off by default.
	Budget BudgetConfig `toml:"budget"`
}

// Default returns a config with sensible defaults: an offline mock Backend and
// a Go build+test Gate.
func Default() Config {
	return Config{
		Repo:        "./repo",
		Backend:     "mock",
		Concurrency: 4,
		MaxAttempts: 2,
		BestOfN:     1,
		Verify: [][]string{
			{"go", "build", "./..."},
			{"go", "test", "./..."},
		},
		Delivery: DeliveryConfig{Mode: "local"},
		Budget:   BudgetConfig{}, // every limit off; require_run_budget false
	}
}

// Load reads aoa.toml from path and fills any unset fields with defaults.
func Load(path string) (Config, error) {
	c := Config{}
	if _, err := toml.DecodeFile(path, &c); err != nil {
		return Config{}, fmt.Errorf("load %s: %w", path, err)
	}
	c = c.withDefaults()
	if m := c.Delivery.Mode; m != "local" && m != "pr" {
		return Config{}, fmt.Errorf("load %s: [delivery] mode %q: want \"local\" or \"pr\"", path, m)
	}
	return c, nil
}

// LoadPricing reads a standalone TOML file holding a [pricing] table (the same
// shape as Config.Pricing: model id -> USD per million tokens). Used by
// `aoa eval --price-file` so a multi-model run can be costed per model.
func LoadPricing(path string) (map[string]float64, error) {
	var f struct {
		Pricing map[string]float64 `toml:"pricing"`
	}
	if _, err := toml.DecodeFile(path, &f); err != nil {
		return nil, fmt.Errorf("load pricing %s: %w", path, err)
	}
	return f.Pricing, nil
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
	if c.BestOfN <= 0 {
		c.BestOfN = d.BestOfN
	}
	if c.Verify == nil {
		c.Verify = d.Verify
	}
	if c.Delivery.Mode == "" {
		c.Delivery.Mode = d.Delivery.Mode
	}
	if c.Delivery.Mode == "pr" {
		if c.Delivery.Remote == "" {
			c.Delivery.Remote = "origin"
		}
		if c.Delivery.Base == "" {
			c.Delivery.Base = "main"
		}
		if c.Delivery.OpenPR == nil {
			c.Delivery.OpenPR = slices.Clone(DefaultOpenPR)
		}
	}
	return c
}
