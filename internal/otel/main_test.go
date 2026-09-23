package otel

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestMain drops every OTEL_* variable before the suite runs. Tests such as
// TestEnabledTracksEndpointEnv assert that no OTLP endpoint is configured, but
// t.Setenv cannot unset a variable, so a shell that exports OTEL_* would fail
// the suite on a clean checkout. Tests that need an endpoint set one themselves.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	if err := unsetOTELEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		return 1
	}
	return m.Run()
}

// unsetOTELEnv removes every environment variable whose name starts with
// OTEL_, so the suite does not inherit a developer's OTLP configuration.
func unsetOTELEnv() error {
	for _, kv := range os.Environ() {
		name, _, found := strings.Cut(kv, "=")
		if !found || !strings.HasPrefix(name, "OTEL_") {
			continue
		}
		if err := os.Unsetenv(name); err != nil {
			return fmt.Errorf("unset %s: %w", name, err)
		}
	}
	return nil
}

func TestUnsetOTELEnvClearsExportedSettings(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:9")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://127.0.0.1:9")
	t.Setenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "http://127.0.0.1:9")
	t.Setenv("OTEL_SERVICE_NAME", "leaked")
	// The prefix is OTEL_, so a bare OTEL and a name that merely contains it stay.
	t.Setenv("OTEL", "keep")
	t.Setenv("NOT_OTEL_FOO", "keep")

	if err := unsetOTELEnv(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_SERVICE_NAME",
	} {
		if _, ok := os.LookupEnv(name); ok {
			t.Errorf("%s still set", name)
		}
	}
	if got := os.Getenv("OTEL"); got != "keep" {
		t.Errorf("OTEL = %q, want keep", got)
	}
	if got := os.Getenv("NOT_OTEL_FOO"); got != "keep" {
		t.Errorf("NOT_OTEL_FOO = %q, want keep", got)
	}
}
