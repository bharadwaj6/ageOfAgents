package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// dispatchCases reads the subcommand names out of main()'s switch. Scanning the
// source is unlovely, but the alternative is a completion list that silently
// rots: nothing else fails when someone adds a command and forgets to complete
// it, and that is exactly the bug this guards.
func dispatchCases(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "switch os.Args[1] {")
	if start < 0 {
		t.Fatal("could not find the dispatch switch in main.go — has main() been restructured?")
	}
	end := strings.Index(body[start:], "\n\t}")
	if end < 0 {
		t.Fatal("could not find the end of the dispatch switch")
	}

	re := regexp.MustCompile(`case ((?:"[^"]+",?\s*)+):`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(body[start:start+end], -1) {
		for _, lit := range strings.Split(m[1], ",") {
			name := strings.Trim(strings.TrimSpace(lit), `"`)
			// Aliases and flag spellings are real dispatch targets but are not
			// worth completing.
			if name == "" || strings.HasPrefix(name, "-") {
				continue
			}
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func TestCompletionListsEveryDispatchableCommand(t *testing.T) {
	known := map[string]bool{}
	for _, c := range commands {
		known[c] = true
	}
	// `feed` is a deprecated alias; completing it would advertise it.
	known["feed"] = true

	for _, c := range dispatchCases(t) {
		if !known[c] {
			t.Errorf("main() dispatches %q but `aoa completion` never offers it — add it to commands", c)
		}
	}
}

func TestCompletionOffersNothingUndispatchable(t *testing.T) {
	dispatchable := map[string]bool{}
	for _, c := range dispatchCases(t) {
		dispatchable[c] = true
	}
	for _, c := range commands {
		if !dispatchable[c] {
			t.Errorf("`aoa completion` offers %q, but main() does not dispatch it", c)
		}
	}
}

func TestCompletionRejectsAnUnknownShell(t *testing.T) {
	if err := cmdCompletion([]string{"tcsh"}); err == nil {
		t.Fatal("an unsupported shell should be an error, not an empty script")
	}
}
