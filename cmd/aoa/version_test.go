package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveBuild(t *testing.T) {
	info := func(ver string, settings ...debug.BuildSetting) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Version: ver}, Settings: settings}
	}
	rev := debug.BuildSetting{Key: "vcs.revision", Value: "f0d9693abcdef"}
	when := debug.BuildSetting{Key: "vcs.time", Value: "2026-09-01T12:00:00Z"}

	tests := []struct {
		name                        string
		ldVersion, ldCommit, ldDate string
		info                        *debug.BuildInfo
		want                        string
	}{
		{
			name:      "ldflags win over build info",
			ldVersion: "0.4.0",
			ldCommit:  "deadbee",
			ldDate:    "2026-08-01T00:00:00Z",
			info:      info("v9.9.9", rev, when),
			want:      "aoa 0.4.0 (deadbee, 2026-08-01T00:00:00Z)",
		},
		{
			name:      "dev with no build info stays dev",
			ldVersion: "dev",
			want:      "aoa dev",
		},
		{
			name:      "go install module version",
			ldVersion: "dev",
			info:      info("v0.4.0"),
			want:      "aoa 0.4.0",
		},
		{
			name:      "devel module version stays dev",
			ldVersion: "dev",
			info:      info("(devel)"),
			want:      "aoa dev",
		},
		{
			name:      "go install version plus vcs metadata",
			ldVersion: "dev",
			info:      info("v0.4.0", rev, when),
			want:      "aoa 0.4.0 (f0d9693, 2026-09-01T12:00:00Z)",
		},
		{
			name:      "local git build keeps dev and reports vcs",
			ldVersion: "dev",
			info:      info("(devel)", rev, when),
			want:      "aoa dev (f0d9693, 2026-09-01T12:00:00Z)",
		},
		{
			name:      "short revision is not truncated",
			ldVersion: "dev",
			info:      info("v0.3.0", debug.BuildSetting{Key: "vcs.revision", Value: "abc1234"}),
			want:      "aoa 0.3.0 (abc1234)",
		},
		{
			name:      "ldflags commit not overwritten by vcs",
			ldVersion: "dev",
			ldCommit:  "cafebab",
			info:      info("v0.4.0", rev, when),
			want:      "aoa 0.4.0 (cafebab, 2026-09-01T12:00:00Z)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatVersion(resolveBuild(tt.ldVersion, tt.ldCommit, tt.ldDate, tt.info))
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
