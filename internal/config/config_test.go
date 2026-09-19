package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
		Delivery:        DeliveryConfig{Mode: "local"},
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

func TestDeliveryDefaults(t *testing.T) {
	tests := []struct {
		name    string
		toml    string
		want    DeliveryConfig
		wantErr string
	}{
		{name: "absent is local", toml: "", want: DeliveryConfig{Mode: "local"}},
		{name: "local leaves the rest unset", toml: "[delivery]\nmode = \"local\"\n", want: DeliveryConfig{Mode: "local"}},
		{
			name: "pr fills remote, base and the gh opener",
			toml: "[delivery]\nmode = \"pr\"\n",
			want: DeliveryConfig{Mode: "pr", Remote: "origin", Base: "main", OpenPR: DefaultOpenPR},
		},
		{
			name: "pr keeps what is set",
			toml: "[delivery]\nmode = \"pr\"\nremote = \"upstream\"\nbase = \"trunk\"\nopen_pr = [\"glab\", \"mr\", \"create\", \"{branch}\"]\n",
			want: DeliveryConfig{Mode: "pr", Remote: "upstream", Base: "trunk", OpenPR: []string{"glab", "mr", "create", "{branch}"}},
		},
		{
			name: "an empty opener means push only",
			toml: "[delivery]\nmode = \"pr\"\nopen_pr = []\n",
			want: DeliveryConfig{Mode: "pr", Remote: "origin", Base: "main", OpenPR: []string{}},
		},
		{name: "invalid mode", toml: "[delivery]\nmode = \"push\"\n", wantErr: `[delivery] mode "push"`},
	}
	// A push-only config survives a save: open_pr = [] must not come back as
	// unset, which would mean the default opener.
	pushOnly := filepath.Join(t.TempDir(), FileName)
	if err := (Config{Repo: "./x", Delivery: DeliveryConfig{Mode: "pr", OpenPR: []string{}}}).Save(pushOnly); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got, err := Load(pushOnly); err != nil || got.Delivery.OpenPR == nil || len(got.Delivery.OpenPR) != 0 {
		t.Errorf("push-only round trip: open_pr = %#v, err = %v; want [] (push only)", got.Delivery.OpenPR, err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(path, []byte("repo = \"./x\"\n"+tt.toml), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := Load(path)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load error = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !reflect.DeepEqual(got.Delivery, tt.want) {
				t.Errorf("delivery = %#v, want %#v", got.Delivery, tt.want)
			}
		})
	}
}
