// Package bench runs a fixed task suite through the orchestrator under different
// decomposition strategies and reports the docs/design/metrics.md numbers, computed
// purely by replaying the resulting Event Log. It is the controlled, hermetic
// benchmark: every strategy uses the deterministic mock Backend, so the run is
// offline and reproducible, and the comparison isolates the one variable that
// matters here — how work is decomposed and parallelized — while showing that
// the orchestrator's invariants hold for all of them.
//
// The strategies stand in for real-world approaches:
//   - Single    — one monolithic ticket (a naive single-agent run).
//   - PlanFirst — plan ticket then an implementation ticket (speckit + plan-mode style).
//   - Emergent  — a worker decomposes the goal into a parallel dependency graph (aoa).
package bench

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/invariant"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/mergequeue"
	"github.com/bharadwaj6/ageOfAgents/internal/metrics"
	"github.com/bharadwaj6/ageOfAgents/internal/orchestrator"
	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Strategy is a decomposition approach being benchmarked.
type Strategy string

const (
	Single    Strategy = "single"
	PlanFirst Strategy = "planfirst"
	Emergent  Strategy = "emergent"
)

// AllStrategies is the default comparison set.
var AllStrategies = []Strategy{Single, PlanFirst, Emergent}

// Task is one benchmark workload: a goal that produces a set of components.
type Task struct {
	Name       string
	Goal       string
	Components []string // file stems the goal must produce
}

// Suite is the curated Go-native task suite.
func Suite() []Task {
	return []Task{
		{Name: "chat-app", Goal: "build a chat app", Components: []string{"types", "backend", "frontend", "e2e"}},
		{Name: "lru-cache", Goal: "build an LRU cache", Components: []string{"types", "cache", "eviction", "tests"}},
		{Name: "cli-tool", Goal: "build a CLI tool", Components: []string{"args", "commands", "output", "integration"}},
	}
}

// Result is one task × strategy outcome.
type Result struct {
	Task       string          `json:"task"`
	Strategy   Strategy        `json:"strategy"`
	Metrics    metrics.Metrics `json:"metrics"`
	Violations []string        `json:"violations,omitempty"`
}

// RunSuite runs every task under every strategy beneath baseDir (each run gets
// its own isolated workspace) and returns the results in a stable order.
func RunSuite(ctx context.Context, baseDir string, tasks []Task, strategies []Strategy) ([]Result, error) {
	var out []Result
	for _, t := range tasks {
		for _, strat := range strategies {
			dir := filepath.Join(baseDir, t.Name+"-"+string(strat))
			r, err := RunTask(ctx, dir, t, strat)
			if err != nil {
				return nil, fmt.Errorf("%s/%s: %w", t.Name, strat, err)
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// RunTask runs one task under one strategy in an isolated workspace at dir and
// returns its replay-derived metrics plus any invariant violations.
func RunTask(ctx context.Context, dir string, t Task, strat Strategy) (Result, error) {
	repo, err := worktree.InitRepo(ctx, filepath.Join(dir, "repo"))
	if err != nil {
		return Result{}, err
	}
	led, err := ledger.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return Result{}, err
	}
	gate := verify.Verifier{Commands: []verify.Command{{"true"}}} // fast hermetic gate
	o := orchestrator.New(led, repo, buildMock(t, strat), mergequeue.New(repo, gate), orchestrator.Options{
		Concurrency:  4,
		WorktreeBase: filepath.Join(dir, "wt"),
	})

	ev, err := api.NewEvent(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: "g-bench", Text: t.Goal})
	if err != nil {
		return Result{}, err
	}
	if _, err := led.Append(ev); err != nil {
		return Result{}, err
	}
	if err := o.Run(ctx); err != nil {
		return Result{}, fmt.Errorf("run: %w", err)
	}

	events, err := led.Read()
	if err != nil {
		return Result{}, err
	}
	r := Result{Task: t.Name, Strategy: strat, Metrics: metrics.Compute(events)}
	for _, v := range invariant.Check(events) {
		r.Violations = append(r.Violations, v.String())
	}
	for _, v := range invariant.MainGreen(ctx, gate, repo.Dir) {
		r.Violations = append(r.Violations, v.String())
	}
	for _, v := range invariant.Settled(events) {
		r.Violations = append(r.Violations, v.String())
	}
	return r, nil
}

// buildMock configures the deterministic Backend so the worker decomposes (or
// not) according to the strategy. The orchestrator seeds one root ticket per
// goal titled "Implement: <goal>"; that is the title the worker acts on.
func buildMock(t Task, strat Strategy) *agent.Mock {
	root := "Implement: " + t.Goal
	m := &agent.Mock{Plan: map[string][]agent.File{}, Decompose: map[string][]agent.Subtask{}}
	switch strat {
	case Single:
		// One monolithic ticket writes every component.
		m.Plan[root] = componentFiles(t.Components)
	case PlanFirst:
		// Plan ticket, then an implementation ticket that depends on it.
		m.Decompose[root] = []agent.Subtask{
			{LocalID: "plan", Title: "plan: " + t.Goal, IdempotencyKey: t.Goal + ":plan"},
			{LocalID: "impl", Title: "impl: " + t.Goal, DependsOn: []string{"plan"}, IdempotencyKey: t.Goal + ":impl"},
		}
		m.Plan["plan: "+t.Goal] = []agent.File{{Path: "PLAN.md", Content: "# Plan for " + t.Goal + "\n"}}
		m.Plan["impl: "+t.Goal] = componentFiles(t.Components)
	case Emergent:
		// A parallel dependency graph: a base component, independent middle
		// components, and an integration component that joins them.
		m.Decompose[root] = diamondSubtasks(t)
		// Leaf tickets have no Plan/Decompose entry, so the mock writes a marker
		// file per leaf — a real, mergeable diff with no cross-leaf conflicts.
	}
	return m
}

func componentFiles(components []string) []agent.File {
	files := make([]agent.File, 0, len(components))
	for _, c := range components {
		files = append(files, agent.File{Path: c + ".txt", Content: c + "\n"})
	}
	return files
}

// diamondSubtasks builds a base -> {middle...} -> integration graph from the
// task's components, maximizing independent (parallelizable) work.
func diamondSubtasks(t Task) []agent.Subtask {
	cs := t.Components
	if len(cs) < 2 {
		// Nothing to parallelize; one child.
		if len(cs) == 1 {
			return []agent.Subtask{{LocalID: cs[0], Title: cs[0], IdempotencyKey: t.Goal + ":" + cs[0]}}
		}
		return nil
	}
	base, last := cs[0], cs[len(cs)-1]
	middle := cs[1 : len(cs)-1]

	subs := []agent.Subtask{{LocalID: base, Title: base, IdempotencyKey: t.Goal + ":" + base}}
	for _, c := range middle {
		subs = append(subs, agent.Subtask{LocalID: c, Title: c, DependsOn: []string{base}, IdempotencyKey: t.Goal + ":" + c})
	}
	deps := middle
	if len(deps) == 0 {
		deps = []string{base}
	}
	subs = append(subs, agent.Subtask{LocalID: last, Title: last, DependsOn: deps, IdempotencyKey: t.Goal + ":" + last})
	return subs
}
