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
