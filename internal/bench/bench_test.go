package bench

import (
	"context"
	"os/exec"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func TestRunSuiteHoldsInvariants(t *testing.T) {
	requireGit(t)
	results, err := RunSuite(context.Background(), t.TempDir(), Suite(), []Strategy{Single, PlanFirst, Emergent})
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	if len(results) != len(Suite())*len([]Strategy{Single, PlanFirst, Emergent}) {
		t.Fatalf("got %d results, want %d", len(results), len(Suite())*len([]Strategy{Single, PlanFirst, Emergent}))
	}
	for _, r := range results {
		if len(r.Violations) != 0 {
			t.Errorf("%s/%s: invariant violations: %v", r.Task, r.Strategy, r.Violations)
		}
		if r.Metrics.CoordinationSessions != 0 {
			t.Errorf("%s/%s: coordination sessions = %d, want 0", r.Task, r.Strategy, r.Metrics.CoordinationSessions)
		}
		if r.Metrics.MergeCorrectness != 1.0 {
			t.Errorf("%s/%s: merge correctness = %v, want 1.0", r.Task, r.Strategy, r.Metrics.MergeCorrectness)
		}
		if r.Metrics.Merged == 0 {
			t.Errorf("%s/%s: nothing merged", r.Task, r.Strategy)
		}
	}
}

func TestEmergentUnlocksParallelism(t *testing.T) {
	requireGit(t)
	task := Suite()[0] // chat-app: 4 components
	ctx := context.Background()

	single, err := RunTask(ctx, t.TempDir(), task, Single)
	if err != nil {
		t.Fatalf("single: %v", err)
	}
	emergent, err := RunTask(ctx, t.TempDir(), task, Emergent)
	if err != nil {
		t.Fatalf("emergent: %v", err)
	}

	if single.Metrics.MaxConcurrentWorkers != 1 {
		t.Errorf("single max concurrent = %d, want 1 (no parallelism)", single.Metrics.MaxConcurrentWorkers)
	}
	if emergent.Metrics.MaxConcurrentWorkers <= single.Metrics.MaxConcurrentWorkers {
		t.Errorf("emergent (%d) should achieve more parallelism than single (%d)",
			emergent.Metrics.MaxConcurrentWorkers, single.Metrics.MaxConcurrentWorkers)
	}
	if emergent.Metrics.EmergentTickets == 0 {
		t.Error("emergent strategy should create tickets at runtime")
	}
	if single.Metrics.EmergentTickets != 0 {
		t.Errorf("single strategy should not decompose, got %d emergent tickets", single.Metrics.EmergentTickets)
	}
}
