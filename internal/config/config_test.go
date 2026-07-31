package config

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	want := Config{
		Repo:            "./demo",
		Backend:         "claudecode",
		Concurrency:     8,
		MaxAttempts:     3,
		BestOfN:         1,
		ConventionsFile: "CONVENTIONS.md",
		Verify:          [][]string{{"go", "build", "./..."}, {"go", "test", "./..."}},
	}
	if err := want.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", got, want)
	}
}

func TestLoadAppliesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	// Minimal config: only the repo is set.
	if err := (Config{Repo: "./x"}).Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Backend != "mock" || got.Concurrency != 4 || got.MaxAttempts != 2 {
		t.Errorf("defaults not applied: %+v", got)
	}
	if len(got.Verify) == 0 {
		t.Error("expected default verify gate")
	}
}

func TestDefaultIsUsable(t *testing.T) {
	d := Default()
	if d.Backend != "mock" || d.Concurrency <= 0 || len(d.Verify) == 0 {
		t.Errorf("Default not usable: %+v", d)
	}
}

func TestTerminationGatesRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	want := Config{
		Repo:              "./demo",
		StallTimeout:      "10m",
		MaxPasses:         50,
		MaxGraphDepth:     3,
		MaxTicketsPerGoal: 16,
		MaxFanOut:         4,
	}
	if err := want.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.StallTimeout != "10m" || got.MaxPasses != 50 || got.MaxGraphDepth != 3 ||
		got.MaxTicketsPerGoal != 16 || got.MaxFanOut != 4 {
		t.Errorf("termination gates did not round-trip: %+v", got)
	}
}

func TestTerminationGatesDefaultToZero(t *testing.T) {
	// Zero is the "use the Scheduler's default" signal; withDefaults must not
	// invent values here, or the authoritative defaults in orchestrator.New
	// would be shadowed by a second copy that can drift.
	path := filepath.Join(t.TempDir(), FileName)
	if err := (Config{Repo: "./x"}).Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.StallTimeout != "" || got.MaxPasses != 0 || got.MaxGraphDepth != 0 ||
		got.MaxTicketsPerGoal != 0 || got.MaxFanOut != 0 {
		t.Errorf("unset termination gates should stay zero, got %+v", got)
	}
}
