package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain runs the package under a deliberately hostile git configuration:
// commit signing on, with a signing program that does not exist. Every git call
// the tests make, directly or through internal/worktree, must therefore be
// independent of the user's own config, as AGENTS.md requires. A test helper
// that forgets used to pass on machines without signing and fail on the ones
// with it. Here it fails everywhere, CI included.
func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	dir, err := os.MkdirTemp("", "aoa-gitconfig-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		return 1
	}
	cfg := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(cfg, []byte("[commit]\n\tgpgsign = true\n[gpg]\n\tprogram = /nonexistent-gpg\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
		return 1
	}
	for k, v := range map[string]string{"GIT_CONFIG_GLOBAL": cfg, "GIT_CONFIG_NOSYSTEM": "1"} {
		if err := os.Setenv(k, v); err != nil {
			fmt.Fprintln(os.Stderr, "TestMain:", err)
			return 1
		}
	}
	code := m.Run()
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintln(os.Stderr, "TestMain:", err)
	}
	return code
}
