// Package liveeval runs the orchestrator end-to-end against a real git
// repository and scores the outcome, closing the gap the controlled bench
// (internal/bench) deliberately leaves open: bench proves the coordination
// machinery is correct using the deterministic mock Backend; liveeval measures
// whether that machinery delivers real task success when an actual agent does
// the work.
//
// It is backend-agnostic by construction. Pass agent.NewMock() and the whole
// run is hermetic and offline (how the tests exercise it); pass
// agent.NewClaudeCode() and a checked-out repository for a live run. Network
// access, API keys, and cost are entirely a property of the Backend you supply
// — the default test suite never uses a networked one (ADR 009).
package liveeval

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/diagnose"
	"github.com/bharadwaj6/ageOfAgents/internal/invariant"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/mergequeue"
	"github.com/bharadwaj6/ageOfAgents/internal/metrics"
	"github.com/bharadwaj6/ageOfAgents/internal/orchestrator"
	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Task is one end-to-end evaluation workload against a real repository.
type Task struct {
	Name string `toml:"name"`
	// RepoDir is an existing git repository (already checked out at the starting
	// commit) that the run treats as the integration repo. The caller clones /
	// prepares it; liveeval never reaches the network itself.
	RepoDir string `toml:"repo_dir"`
	// Goal is the objective handed to the orchestrator.
	Goal string `toml:"goal"`
	// Gate is the merge Gate (build/tests/lint) every proposal must pass.
	Gate [][]string `toml:"gate"`
	// Success is the oracle: commands that must pass on the final `main` for the
	// task to count as solved (e.g. the issue's reproduce test). When empty, a
	// run counts as successful if it merged work with no invariant violations.
	Success [][]string `toml:"success"`
	// Regression is an optional broader test set run post-merge (the Shadow gate)
	// to measure the regression-escape rate — merges the Gate accepted but a
	// wider suite would reject. Reported in Metrics.RegressionEscapeRate.
	Regression [][]string `toml:"regression"`
	// Sandbox, SandboxImage and SandboxMount isolate this task's Gate the same way
	// the aoa.toml fields of those names do. They are per-task because a benchmark
	// run gates each task in the image prepared for that task's repository.
	Sandbox      string `toml:"sandbox"`
	SandboxImage string `toml:"sandbox_image"`
	SandboxMount string `toml:"sandbox_mount"`
}

// Report is one task's outcome, derived almost entirely by replaying the log.
type Report struct {
	Task        string          `json:"task"`
	Backend     string          `json:"backend"`
	Success     bool            `json:"success"`
	Metrics     metrics.Metrics `json:"metrics"`
	MAST        diagnose.Report `json:"mast"`
	Violations  []string        `json:"violations,omitempty"`
	AgentErrors []string        `json:"agent_errors,omitempty"`
}

// TaskFile is the on-disk format for `aoa eval --tasks`.
type TaskFile struct {
	Tasks []Task `toml:"task"`
}

// LoadTasks reads a TOML task file.
func LoadTasks(path string) ([]Task, error) {
	var f TaskFile
	if _, err := toml.DecodeFile(path, &f); err != nil {
		return nil, fmt.Errorf("load tasks %s: %w", path, err)
	}
	return f.Tasks, nil
}

// Run executes one task end-to-end with the given Backend in an isolated
// workspace under baseDir, then evaluates the Success oracle on the resulting
// `main`. Metrics, the MAST histogram, and invariant violations are all derived
// from the run's Event Log.
func Run(ctx context.Context, backend agent.Backend, baseDir string, t Task) (Report, error) {
	rep := Report{Task: t.Name, Backend: backend.Name()}

	repo := worktree.OpenRepo(t.RepoDir)
	led, err := ledger.Open(filepath.Join(baseDir, "events.jsonl"))
	if err != nil {
		return rep, err
	}
	sandbox := func(cmds [][]string) verify.Verifier {
		return verify.Verifier{
			Commands: verify.ToCommands(cmds),
			Sandbox:  t.Sandbox,
			Image:    t.SandboxImage,
			Mount:    t.SandboxMount,
		}
	}
	gate := sandbox(t.Gate)
	mq := mergequeue.New(repo, gate)
	mq.Shadow = sandbox(t.Regression)
	o := orchestrator.New(led, repo, backend, mq, orchestrator.Options{
		Concurrency:  4,
		WorktreeBase: filepath.Join(baseDir, "wt"),
	})

	ev, err := api.NewEvent(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: "g-eval", Text: t.Goal})
	if err != nil {
		return rep, err
	}
	if _, err := led.Append(ev); err != nil {
		return rep, err
	}
	if err := o.Run(ctx); err != nil {
		return rep, fmt.Errorf("run: %w", err)
	}

	events, err := led.Read()
	if err != nil {
		return rep, err
	}
	rep.Metrics = metrics.Compute(events)
	rep.MAST = diagnose.Classify(events)
	for _, v := range invariant.Check(events) {
		rep.Violations = append(rep.Violations, v.String())
	}
	for _, v := range invariant.MainGreen(ctx, gate, repo.Dir) {
		rep.Violations = append(rep.Violations, v.String())
	}
	rep.AgentErrors = collectFailureReasons(events)

	// Success oracle: an explicit command set if given, else "merged something
	// without breaking any invariant".
	if len(t.Success) > 0 {
		oracle := sandbox(t.Success)
		rep.Success = oracle.Run(ctx, repo.Dir).Passed && len(rep.Violations) == 0
	} else {
		rep.Success = rep.Metrics.Merged > 0 && len(rep.Violations) == 0
	}
	return rep, nil
}

// collectFailureReasons extracts the Reason field from every TicketFailed event
// in the log. This surfaces agent/worktree/commit error messages that would
// otherwise be lost when the eval workspace is cleaned up.
func collectFailureReasons(events []api.Event) []string {
	var reasons []string
	for _, e := range events {
		if e.Type != api.TicketFailed {
			continue
		}
		var p api.TicketFailedPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil || p.Reason == "" {
			continue
		}
		reasons = append(reasons, p.Reason)
	}
	return reasons
}
