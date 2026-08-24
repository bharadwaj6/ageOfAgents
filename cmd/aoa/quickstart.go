package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

// cmdQuickstart is the shortest path from "cloned it" to "watched it work". It
// is deliberately a wrapper over the real commands rather than a special mode:
// it prints each one before running it, so what you have just seen is a script
// you can retype. Nothing here is reachable only through quickstart.
func cmdQuickstart(args []string) error {
	fs := flag.NewFlagSet("quickstart", flag.ExitOnError)
	describe(fs,
		"aoa quickstart — scaffold a demo workspace, submit a goal and run it.\n\n"+
			"Runs the whole loop offline on the mock backend: no API key, no cost,\n"+
			"no network. Every step is an ordinary aoa command, printed as it runs.",
		"aoa quickstart --path ./workspace")
	path := fs.String("path", "./workspace", "workspace root to create")
	goal := fs.String("goal", "add a greeting function", "the goal to submit")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(*path, "aoa.toml")); err == nil {
		return fmt.Errorf("%s is already a workspace — run `aoa run --path %s`, or pick another --path", *path, *path)
	}

	repo := filepath.Join(*path, "demo")
	steps := []struct {
		blurb string
		argv  []string
		run   func([]string) error
	}{
		{"Scaffolding a throwaway git repo and an Event Log",
			[]string{"init", "--path", *path, "--repo", repo}, cmdInit},
		{"Submitting a Goal",
			[]string{"goal", "--path", *path, *goal}, cmdGoal},
		{"Running the Scheduler until everything settles",
			[]string{"run", "--path", *path}, cmdRun},
		{"What happened",
			[]string{"status", "--path", *path}, cmdStatus},
	}

	for i, s := range steps {
		fmt.Printf("\n[%d/%d] %s\n      $ aoa %s\n\n", i+1, len(steps), s.blurb, quote(s.argv))
		if err := s.run(s.argv[1:]); err != nil {
			return fmt.Errorf("%s: %w", s.argv[0], err)
		}
	}

	fmt.Printf(`
Done. The default backend is "mock" — a fixture, not a tiny model: it writes one
placeholder file named after the Task. So a g-….txt was just committed to main,
and that is the point. You watched the real machinery run end to end — isolated
worktree, your Gate, serialized merge, every step recorded in the Event Log.

Next:
  aoa events --path %s tail     # the log all of that state was derived from
  aoa doctor --path %s          # check this machine can run a real backend

To do real work, point it at your own repo and pick a backend:
  aoa init --path ./ws --adopt /path/to/your/repo
  # then set backend = "claudecode" (or grok, codex, cursor) in ./ws/aoa.toml
`, *path, *path)
	return nil
}

// quote renders an argv for display, quoting only the elements that need it.
func quote(argv []string) string {
	out := ""
	for i, a := range argv {
		if i > 0 {
			out += " "
		}
		if a == "" || hasSpace(a) {
			out += `"` + a + `"`
			continue
		}
		out += a
	}
	return out
}

func hasSpace(s string) bool {
	for _, r := range s {
		if r == ' ' || r == '\t' {
			return true
		}
	}
	return false
}
