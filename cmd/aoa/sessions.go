package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// sessionID derives a stable ID from a worktree's path and the time it was
// created, so every observation of one worktree folds into one session, and a
// worktree removed and re-created at the same path is a new one (ADR 022).
func sessionID(path string, born time.Time) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d", filepath.Clean(path), born.UnixNano()))
	return "s-" + hex.EncodeToString(sum[:4])
}

// aoaBranchPrefix marks the worktrees aoa creates itself, for an attempt or a
// Goal branch. Their work is on the workspace's own Event Log already.
const aoaBranchPrefix = "aoa/"

// sessionLedgerPath is where a repository's sessions are recorded: inside the
// git directory its worktrees share, so one log covers them all and nothing
// is written into any working tree.
func sessionLedgerPath(ctx context.Context, repo string) (string, error) {
	common, err := worktree.CommonDir(ctx, repo)
	if err != nil {
		return "", fmt.Errorf("%s is not a git repository: %w", repo, err)
	}
	return filepath.Join(common, "aoa", "events.jsonl"), nil
}

// sessionScan is one pass over a repository's working trees.
type sessionScan struct {
	repo, base string
	ids        []string               // parallel to obs, in `git worktree list` order
	obs        []worktree.Observation // every linked worktree that could be read
	listed     []worktree.Listed      // parallel to obs: the entry each was read from
	present    map[string]bool        // every session whose worktree exists, readable or not
	skipped    map[string]bool        // paths whose worktree exists but has no identity
	ownCount   int                    // worktrees on aoa/* branches, left to aoa's own log
	warnings   []string               // worktrees that could not be read, and why
}

// scanSessions reads every linked working tree of repo. The main working tree
// is the integration checkout, not a session, so it is skipped; it supplies
// the default base instead.
func scanSessions(ctx context.Context, repo, base string) (sessionScan, error) {
	sc := sessionScan{repo: repo, base: base, present: map[string]bool{}, skipped: map[string]bool{}}
	list, err := worktree.ListWorktrees(ctx, repo)
	if err != nil {
		return sc, fmt.Errorf("%s is not a git repository: %w", repo, err)
	}
	if len(list) == 0 {
		return sc, fmt.Errorf("git listed no working trees for %s", repo)
	}
	main := list[0]
	sc.repo = main.Path
	if sc.base == "" {
		if sc.base = main.Branch; sc.base == "" {
			sc.base = main.Head // the main worktree is detached
		}
	}
	for _, wt := range list[1:] {
		if wt.Bare {
			continue
		}
		if wt.Prunable {
			continue // its directory is gone: it counts as removed, not present
		}
		if strings.HasPrefix(wt.Branch, aoaBranchPrefix) {
			sc.ownCount++
			continue
		}
		born, err := worktree.Born(wt.Path)
		if err != nil {
			// Unreadable is not gone: keep whatever session is at this path.
			sc.skipped[filepath.Clean(wt.Path)] = true
			sc.warnings = append(sc.warnings, fmt.Sprintf("%s: %v", wt.Path, err))
			continue
		}
		id := sessionID(wt.Path, born)
		sc.present[id] = true
		o, err := worktree.Observe(ctx, wt, sc.base)
		if err != nil {
			sc.warnings = append(sc.warnings, fmt.Sprintf("%s: %v", wt.Path, err))
			continue
		}
		sc.ids = append(sc.ids, id)
		sc.obs = append(sc.obs, o)
		sc.listed = append(sc.listed, wt)
	}
	return sc, nil
}

// recordSessions appends what the scan learned that the log does not already
// say: a changed session, a new one, or one whose worktree has gone. The fold
// and the decision happen under the ledger lock, so two `aoa sessions` racing
// each other record one observation, not two.
func recordSessions(led *ledger.Ledger, sc sessionScan) ([]api.Event, error) {
	return led.Update(func(events []api.Event) ([]api.Event, error) {
		st, err := state.Fold(events)
		if err != nil {
			return nil, err
		}
		var pending []api.Event
		for i, o := range sc.obs {
			p := api.SessionObservedPayload{
				SessionID: sc.ids[i], Path: o.Path, Branch: o.Branch, Head: o.Head,
				Base: o.Base, Files: o.Files, TestFiles: o.TestFiles, Dirty: o.Dirty,
				Fingerprint: o.Fingerprint, LastChange: o.LastChange,
			}
			if x := st.Sessions[p.SessionID]; x != nil && x.Same(p) {
				continue
			}
			ev, err := api.NewEvent(api.SessionObserved, "sessions", p)
			if err != nil {
				return nil, err
			}
			pending = append(pending, ev)
		}
		for _, x := range st.OrderedSessions() {
			if x.Removed || sc.present[x.ID] || sc.skipped[filepath.Clean(x.Path)] {
				continue
			}
			ev, err := api.NewEvent(api.SessionObserved, "sessions", api.SessionObservedPayload{
				SessionID: x.ID, Path: x.Path, Removed: true,
			})
			if err != nil {
				return nil, err
			}
			pending = append(pending, ev)
		}
		return pending, nil
	})
}

// observeAndRecord is the scan-then-append both subcommands start with.
func observeAndRecord(ctx context.Context, repo, base string) (sessionScan, *state.State, error) {
	sc, err := scanSessions(ctx, repo, base)
	if err != nil {
		return sc, nil, err
	}
	path, err := sessionLedgerPath(ctx, sc.repo)
	if err != nil {
		return sc, nil, err
	}
	led, err := ledger.Open(path)
	if err != nil {
		return sc, nil, err
	}
	if _, err := recordSessions(led, sc); err != nil {
		return sc, nil, err
	}
	events, err := led.Read()
	if err != nil {
		return sc, nil, err
	}
	st, err := state.Fold(events)
	return sc, st, err
}

func cmdSessions(args []string) error {
	if len(args) > 0 && args[0] == "check" {
		return cmdSessionsCheck(args[1:])
	}
	fs := flag.NewFlagSet("sessions", flag.ExitOnError)
	describe(fs, "aoa sessions \u2014 what every agent session in this repository has done,\nincluding sessions aoa did not start.\n\n"+
		"Each linked git worktree is one session. Reading is all it does: it\nrecords what git shows \u2014 the files changed against the base branch, which\n"+
		"of them are tests, and whether the Gate has passed \u2014 into an Event Log\nunder the repository's git directory. Nothing is dispatched or merged.\n\n"+
		"Subcommand: check (run the Gate on one session's working tree).",
		"aoa sessions --repo ~/Projects/myrepo")
	repo := fs.String("repo", ".", "repository to read (any of its worktrees)")
	base := fs.String("base", "", "ref changes are measured against (default: the main worktree's branch)")
	all := fs.Bool("all", false, "also list sessions whose worktree has been removed")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q (the only subcommand is `check`)", fs.Arg(0))
	}
	sc, st, err := observeAndRecord(context.Background(), *repo, *base)
	if err != nil {
		return err
	}
	return renderSessions(os.Stdout, sc, st, time.Now(), *all)
}

func cmdSessionsCheck(args []string) error {
	fs := flag.NewFlagSet("sessions check", flag.ExitOnError)
	describe(fs, "aoa sessions check \u2014 run the Gate on one session's working tree and\nrecord the verdict.\n\n"+
		"This is a report, not a merge: it tells you whether that tree is green\nright now, and says nothing about the integration branch. The verdict is\n"+
		"recorded against the tree's current content, so `aoa sessions` marks it\nstale once the session edits anything else.\n\n"+
		"Name the session by ID, by a unique prefix of it, by branch, or by path.",
		"aoa sessions check --gate \"go test ./...\" agent/login")
	repo := fs.String("repo", ".", "repository to read (any of its worktrees)")
	base := fs.String("base", "", "ref changes are measured against (default: the main worktree's branch)")
	var gate gateFlag
	fs.Var(&gate, "gate", "a Gate command to run in the worktree, split on spaces (no shell);\nrepeat for several. Default: detected from the project")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("usage: aoa sessions check [flags] <session-id|branch|path>")
	}
	if fs.NArg() > 1 {
		return fmt.Errorf("unexpected argument %q: check takes one session", fs.Arg(1))
	}
	target := fs.Arg(0)
	if err := rejectStrayFlags(fs.Args()[1:]); err != nil {
		return err
	}

	ctx := context.Background()
	sc, _, err := observeAndRecord(ctx, *repo, *base)
	if err != nil {
		return err
	}
	i, err := findSession(sc, target)
	if err != nil {
		return err
	}
	id, o := sc.ids[i], sc.obs[i]

	commands := [][]string(gate)
	if len(commands) == 0 {
		detected, lang := detectGate(o.Path)
		if len(detected) == 0 {
			return fmt.Errorf("no Gate detected in %s (%s): pass --gate \"<command>\"", o.Path, lang)
		}
		commands = detected
	}
	fmt.Printf("%s  %s\n  gate: %s\n\n", id, o.Branch, gateString(commands))
	res := verify.Verifier{Commands: verify.ToCommands(commands)}.Run(ctx, o.Path)
	after, note, err := afterGate(ctx, sc.listed[i], sc.base, o)
	if err != nil {
		return err
	}

	path, err := sessionLedgerPath(ctx, sc.repo)
	if err != nil {
		return err
	}
	led, err := ledger.Open(path)
	if err != nil {
		return err
	}
	ev, err := api.NewEvent(api.SessionChecked, "sessions", api.SessionCheckedPayload{
		SessionID: id, Fingerprint: o.Fingerprint, AfterFingerprint: after, Passed: res.Passed,
		Command: gateString(commands), Output: lastLines(res.Output, 40),
	})
	if err != nil {
		return err
	}
	if _, err := led.Append(ev); err != nil {
		return err
	}

	if note != "" {
		fmt.Println(note)
	}
	if res.Passed {
		fmt.Println("Gate passed.")
		return nil
	}
	fmt.Print(lastLines(res.Output, 20))
	if !strings.HasSuffix(res.Output, "\n") {
		fmt.Println()
	}
	return fmt.Errorf("Gate failed on %s (%s)", id, res.Failed)
}

// afterGate re-reads a session's tree once its Gate has run. When only
// untracked files appeared, the Gate wrote them — coverage, build output — and
// the tree it judged is unchanged, so the verdict also holds for the tree as it
// is now: after is that tree's fingerprint. When HEAD or a tracked file moved,
// something edited the tree while the Gate ran, and the verdict covers only
// what it saw: after is empty, so the next observation shows it stale. note
// says which happened, or is empty when nothing did.
func afterGate(ctx context.Context, wt worktree.Listed, base string, before worktree.Observation) (after, note string, err error) {
	now, err := worktree.Observe(ctx, wt, base)
	if err != nil {
		return "", "", fmt.Errorf("re-read %s after the Gate: %w", wt.Path, err)
	}
	switch {
	case now.Fingerprint == before.Fingerprint:
		return "", "", nil
	case now.Tracked != before.Tracked:
		return "", "note: the tree changed while the Gate ran; the verdict covers what it saw, and shows stale", nil
	}
	had := map[string]bool{}
	for _, f := range before.Untracked {
		had[f] = true
	}
	var added []string
	for _, f := range now.Untracked {
		if !had[f] {
			added = append(added, f)
		}
	}
	note = "note: the Gate rewrote untracked files; the verdict still holds"
	if len(added) > 0 {
		note = fmt.Sprintf("note: the Gate left untracked files: %s \u2014 consider .gitignore", strings.Join(added, ", "))
	}
	return now.Fingerprint, note, nil
}

// findSession resolves what the user typed — an ID, a unique prefix of one, a
// branch, or a path — to an index into the scan.
func findSession(sc sessionScan, target string) (int, error) {
	abs, absErr := filepath.Abs(target)
	var matches []int
	for i, o := range sc.obs {
		switch {
		case sc.ids[i] == target, o.Branch != "" && o.Branch == target:
			return i, nil
		case absErr == nil && filepath.Clean(o.Path) == filepath.Clean(abs):
			return i, nil
		case len(target) >= 3 && strings.HasPrefix(sc.ids[i], target):
			matches = append(matches, i)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		var known []string
		for i, o := range sc.obs {
			known = append(known, fmt.Sprintf("%s (%s)", sc.ids[i], o.Branch))
		}
		if len(known) == 0 {
			return 0, fmt.Errorf("no sessions in %s: it has no linked git worktrees", sc.repo)
		}
		return 0, fmt.Errorf("no session %q; this repository has: %s", target, strings.Join(known, ", "))
	default:
		var amb []string
		for _, i := range matches {
			amb = append(amb, sc.ids[i])
		}
		return 0, fmt.Errorf("%q matches several sessions: %s", target, strings.Join(amb, ", "))
	}
}

// gateFlag collects repeated --gate values as argv, split on spaces. There is
// no shell: a Gate is a command, not a script.
type gateFlag [][]string

func (g *gateFlag) String() string { return gateString(*g) }

func (g *gateFlag) Set(v string) error {
	fields := strings.Fields(v)
	if len(fields) == 0 {
		return fmt.Errorf("--gate takes a command")
	}
	*g = append(*g, fields)
	return nil
}

func renderSessions(w io.Writer, sc sessionScan, st *state.State, now time.Time, all bool) error {
	for _, warn := range sc.warnings {
		fmt.Fprintf(w, "note: skipped %s\n", warn)
	}
	if sc.ownCount > 0 {
		fmt.Fprintf(w, "note: %d worktrees on %s* branches are aoa's own (see aoa status)\n", sc.ownCount, aoaBranchPrefix)
	}
	fmt.Fprintf(w, "Sessions in %s — changes measured against %s\n\n", sc.repo, sc.base)
	// A removed session stays on the log; it is listed only on request, so
	// a day of worktrees made and cleaned up does not bury the live ones.
	var gone []*state.Session
	for _, x := range st.OrderedSessions() {
		if x.Removed {
			gone = append(gone, x)
		}
	}
	hidden := 0
	if !all {
		hidden, gone = len(gone), nil
	}
	if len(sc.obs) == 0 && len(gone) == 0 {
		fmt.Fprintf(w, "No sessions: this repository has no linked git worktrees.\nRun each agent in its own worktree (`git worktree add`) and they show up here.\n")
		sessionsFooter(w, hidden)
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SESSION\tBRANCH\tFILES\tTESTS\tGATE\tLAST CHANGE")
	for i, o := range sc.obs {
		id := sc.ids[i]
		files := fmt.Sprintf("%d", len(o.Files))
		if o.Dirty {
			files += "*"
		}
		tests := "—"
		if n := len(o.TestFiles); n > 0 {
			tests = fmt.Sprintf("%d !", n)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			id, dash(o.Branch), files, tests, gateCell(st.Sessions[id]), ago(now, o.LastChange))
	}
	for _, x := range gone {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\t%s\t%s\n",
			x.ID, dash(x.Branch), len(x.Files), "—", "worktree gone", ago(now, x.LastSeen))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if len(sc.obs) > 0 {
		fmt.Fprintf(w, "\n* uncommitted changes\n")
	}
	sessionsFooter(w, hidden)

	var touched bool
	for i, o := range sc.obs {
		if len(o.TestFiles) == 0 {
			continue
		}
		if !touched {
			fmt.Fprintf(w, "\nTest files touched — the Gate cannot vouch for a tree that rewrote its own tests:\n")
			touched = true
		}
		fmt.Fprintf(w, "  %s  %s\n", sc.ids[i], strings.Join(o.TestFiles, ", "))
	}
	return nil
}

// sessionsFooter says how many removed sessions the table left out.
func sessionsFooter(w io.Writer, hidden int) {
	if hidden > 0 {
		fmt.Fprintf(w, "%d removed sessions hidden (--all shows them)\n", hidden)
	}
}

func gateCell(x *state.Session) string {
	if x == nil || x.Check == nil {
		return "not run"
	}
	cell := "FAIL"
	if x.Check.Passed {
		cell = "pass"
	}
	if x.CheckStale() {
		cell += " (stale)"
	}
	return cell
}

func dash(s string) string {
	if s == "" {
		return "(detached)"
	}
	return s
}

// ago renders how long ago t was, coarsely: a session's age is read at a
// glance, and an exact timestamp is in the Event Log.
func ago(now, t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// lastLines keeps the tail of Gate output: the failure is at the end, and the
// whole log of a big test suite has no business in an event.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n") + "\n"
}
