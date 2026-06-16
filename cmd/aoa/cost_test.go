package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/liveeval"
	"github.com/bharadwaj6/ageOfAgents/internal/metrics"
	"github.com/stretchr/testify/require"
)

func TestEvalCost(t *testing.T) {
	m := metrics.Metrics{
		TokensTotal:   2_000_000,
		TokensByModel: map[string]int{"grok": 1_000_000, "claudecode": 1_000_000},
	}
	// Per-model price map wins: 1M*5 + 1M*15 = $20 (per million).
	if got := evalCost(m, map[string]float64{"grok": 5, "claudecode": 15}, 0); got != 20 {
		t.Errorf("per-model cost = %v, want 20", got)
	}
	// Flat price over total tokens: 2M * $3/M = $6.
	if got := evalCost(m, nil, 3); got != 6 {
		t.Errorf("flat cost = %v, want 6", got)
	}
	// Unpriced => $0.
	if got := evalCost(m, nil, 0); got != 0 {
		t.Errorf("unpriced cost = %v, want 0", got)
	}
}

func TestPrintEvalTableFooterReportsSkipped(t *testing.T) {
	reports := []liveeval.Report{
		{Task: "a", Backend: "mock", Success: true, Metrics: metrics.Metrics{Merged: 1, TokensTotal: 1_000_000}},
		{Task: "b", Backend: "mock", Success: false, Metrics: metrics.Metrics{TokensTotal: 1_000_000}},
	}
	out := captureStdout(t, func() {
		printEvalTable(reports, nil, 10, 3) // flat $10/M, 3 tasks skipped by the cap
	})
	// 2M tokens * $10/M = $20 total; 1 of 2 solved; 5 tasks total (2 ran + 3 skipped).
	for _, want := range []string{"solved=1/2", "tokens=2000000", "cost=$20.0000", "ran 2/5", "skipped 3 by --max-cost"} {
		if !strings.Contains(out, want) {
			t.Errorf("footer missing %q; got:\n%s", want, out)
		}
	}
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what it
// wrote. ponytail: a tiny pipe, no test framework.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()
	fn()
	w.Close()
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(b)
}
