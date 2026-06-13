// Command aoa is the CLI for the Age of Agents orchestrator. It is intentionally
// tiny (standard-library flags, no framework): init a town, submit a goal, run
// the reconciler, and inspect state via status/feed/events.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/config"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/mergequeue"
	"github.com/bharadwaj6/ageOfAgents/internal/orchestrator"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/verify"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	args := os.Args[2:]
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(args)
	case "goal":
		err = cmdGoal(args)
	case "run":
		err = cmdRun(args)
	case "status":
		err = cmdStatus(args)
	case "feed":
		err = cmdFeed(args)
	case "events":
		err = cmdEvents(args)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`aoa - Age of Agents orchestrator

Usage:
  aoa init   [--path DIR] [--repo PATH]   Scaffold a town + integration repo
  aoa goal   [--path DIR] "objective"     Submit a goal
  aoa run    [--path DIR] [--once]        Run the reconciler (loop by default)
  aoa status [--path DIR]                 Show goals and tickets
  aoa feed   [--path DIR] [--type T]      Print the event stream
  aoa events [--path DIR] tail [--count N] | replay
`)
}

// town resolves the standard paths for a town root.
type town struct {
	root, configPath, ledgerPath, worktreeBase string
}

func townAt(path string) (town, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return town{}, err
	}
	return town{
		root:         root,
		configPath:   filepath.Join(root, config.FileName),
		ledgerPath:   filepath.Join(root, ".aoa", "events.jsonl"),
		worktreeBase: filepath.Join(root, ".aoa", "worktrees"),
	}, nil
}

func resolve(root, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	path := fs.String("path", ".", "town root")
	repo := fs.String("repo", "./repo", "integration repo path (relative to town root)")
	_ = fs.Parse(args)

	ctx := context.Background()
	tn, err := townAt(*path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(tn.root, ".aoa"), 0o755); err != nil {
		return err
	}

	repoPath := resolve(tn.root, *repo)
	r, err := worktree.InitRepo(ctx, repoPath)
	if err != nil {
		return err
	}
	// Seed a minimal Go module so the default `go build/test` gate is real.
	module := worktree.SanitizeBranch(filepath.Base(repoPath))
	goMod := fmt.Sprintf("module %s\n\ngo %s\n", module, goVersion())
	if err := os.WriteFile(filepath.Join(repoPath, "go.mod"), []byte(goMod), 0o644); err != nil {
		return err
	}
	doc := fmt.Sprintf("// Package %s is scaffolded by aoa.\npackage %s\n", module, module)
	if err := os.WriteFile(filepath.Join(repoPath, "doc.go"), []byte(doc), 0o644); err != nil {
		return err
	}
	if _, _, err := r.CommitAll(ctx, "chore: scaffold Go module"); err != nil {
		return err
	}

	cfg := config.Default()
	cfg.Repo = *repo
	cfg.ConventionsFile = "CONVENTIONS.md"
	if err := cfg.Save(tn.configPath); err != nil {
		return err
	}
	conv := "# Conventions\n\n- Keep changes minimal and focused.\n- Every change must keep `go build ./...` and `go test ./...` green.\n"
	if err := os.WriteFile(filepath.Join(tn.root, "CONVENTIONS.md"), []byte(conv), 0o644); err != nil {
		return err
	}

	fmt.Printf("Initialized town at %s\n  repo:   %s\n  config: %s\n\nNext:\n  aoa goal --path %s \"your objective\"\n  aoa run  --path %s\n",
		tn.root, repoPath, tn.configPath, *path, *path)
	return nil
}

func cmdGoal(args []string) error {
	fs := flag.NewFlagSet("goal", flag.ExitOnError)
	path := fs.String("path", ".", "town root")
	_ = fs.Parse(args)

	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		return fmt.Errorf("goal text is required: aoa goal \"do the thing\"")
	}
	tn, err := townAt(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(tn.ledgerPath)
	if err != nil {
		return err
	}
	goalID := "g-" + shortID()
	ev, err := api.NewEvent(api.GoalSubmitted, "human", api.GoalSubmittedPayload{GoalID: goalID, Text: text})
	if err != nil {
		return err
	}
	if _, err := led.Append(ev); err != nil {
		return err
	}
	fmt.Printf("submitted goal %s: %q\n", goalID, text)
	return nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	path := fs.String("path", ".", "town root")
	once := fs.Bool("once", false, "run a single reconcile pass instead of looping")
	_ = fs.Parse(args)

	tn, err := townAt(*path)
	if err != nil {
		return err
	}
	o, led, err := buildOrchestrator(tn)
	if err != nil {
		return err
	}
	ctx := context.Background()
	if *once {
		if err := o.ReconcileOnce(ctx); err != nil {
			return err
		}
	} else if err := o.Run(ctx); err != nil {
		return err
	}
	return printStatus(led)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	path := fs.String("path", ".", "town root")
	_ = fs.Parse(args)
	tn, err := townAt(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(tn.ledgerPath)
	if err != nil {
		return err
	}
	return printStatus(led)
}

func cmdFeed(args []string) error {
	fs := flag.NewFlagSet("feed", flag.ExitOnError)
	path := fs.String("path", ".", "town root")
	typ := fs.String("type", "", "filter by event type")
	_ = fs.Parse(args)
	tn, err := townAt(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(tn.ledgerPath)
	if err != nil {
		return err
	}
	events, err := led.Read()
	if err != nil {
		return err
	}
	for _, e := range events {
		if *typ != "" && string(e.Type) != *typ {
			continue
		}
		fmt.Println(formatEvent(e))
	}
	return nil
}

func cmdEvents(args []string) error {
	fs := flag.NewFlagSet("events", flag.ExitOnError)
	path := fs.String("path", ".", "town root")
	count := fs.Int("count", 20, "number of events for tail")
	_ = fs.Parse(args)

	sub := "tail"
	if fs.NArg() > 0 {
		sub = fs.Arg(0)
	}
	tn, err := townAt(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(tn.ledgerPath)
	if err != nil {
		return err
	}
	events, err := led.Read()
	if err != nil {
		return err
	}
	switch sub {
	case "tail":
		start := 0
		if len(events) > *count {
			start = len(events) - *count
		}
		for _, e := range events[start:] {
			fmt.Println(formatEvent(e))
		}
	case "replay":
		for _, e := range events {
			fmt.Printf("%s  %s\n", formatEvent(e), string(e.Payload))
		}
	default:
		return fmt.Errorf("unknown events subcommand %q (want tail|replay)", sub)
	}
	return nil
}

// --- wiring ---------------------------------------------------------------

func buildOrchestrator(tn town) (*orchestrator.Orchestrator, *ledger.Ledger, error) {
	cfg, err := config.Load(tn.configPath)
	if err != nil {
		return nil, nil, err
	}
	repo := worktree.OpenRepo(resolve(tn.root, cfg.Repo))
	led, err := ledger.Open(tn.ledgerPath)
	if err != nil {
		return nil, nil, err
	}
	backend, err := buildBackend(cfg.Backend)
	if err != nil {
		return nil, nil, err
	}
	conventions := ""
	if cfg.ConventionsFile != "" {
		if b, err := os.ReadFile(resolve(tn.root, cfg.ConventionsFile)); err == nil {
			conventions = string(b)
		}
	}
	gate := verify.Verifier{Commands: toCommands(cfg.Verify)}
	opt := orchestrator.Options{
		Concurrency:  cfg.Concurrency,
		MaxAttempts:  cfg.MaxAttempts,
		Conventions:  conventions,
		WorktreeBase: tn.worktreeBase,
	}
	o := orchestrator.New(led, repo, backend, mergequeue.New(repo, gate), opt)
	return o, led, nil
}

func buildBackend(name string) (agent.Backend, error) {
	switch name {
	case "mock", "":
		return agent.NewMock(), nil
	case "claudecode":
		return agent.NewClaudeCode(), nil
	default:
		return nil, fmt.Errorf("unknown backend %q (want mock|claudecode)", name)
	}
}

func toCommands(cmds [][]string) []verify.Command {
	out := make([]verify.Command, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, verify.Command(c))
	}
	return out
}

// --- presentation ---------------------------------------------------------

func printStatus(led *ledger.Ledger) error {
	events, err := led.Read()
	if err != nil {
		return err
	}
	s, err := state.Fold(events)
	if err != nil {
		return err
	}
	if len(s.Goals) == 0 {
		fmt.Println("no goals submitted")
		return nil
	}

	goalIDs := make([]string, 0, len(s.Goals))
	for id := range s.Goals {
		goalIDs = append(goalIDs, id)
	}
	sort.Strings(goalIDs)

	for _, gid := range goalIDs {
		fmt.Printf("goal %s: %s\n", gid, s.Goals[gid].Text)
	}
	fmt.Println()

	ticketIDs := make([]string, 0, len(s.Tickets))
	for id := range s.Tickets {
		ticketIDs = append(ticketIDs, id)
	}
	sort.Strings(ticketIDs)

	for _, id := range ticketIDs {
		t := s.Tickets[id]
		fmt.Printf("  [%-8s] %s  (attempts=%d)\n", t.Status, t.ID, t.Attempts)
	}
	if s.Settled() {
		fmt.Println("\nall work settled")
	}
	return nil
}

func formatEvent(e api.Event) string {
	return fmt.Sprintf("#%-4d %-19s %s", e.Seq, e.Type, summarize(e))
}

// summarize extracts a ticket/goal id from common payloads for a compact feed.
func summarize(e api.Event) string {
	var m map[string]any
	if len(e.Payload) > 0 {
		_ = json.Unmarshal(e.Payload, &m)
	}
	for _, k := range []string{"ticket_id", "goal_id"} {
		if v, ok := m[k]; ok {
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

func shortID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func goVersion() string {
	// runtime.Version() looks like "go1.26.4"; go.mod wants "1.26".
	v := strings.TrimPrefix(runtime.Version(), "go")
	parts := strings.Split(v, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return v
}
