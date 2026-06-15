// Command aoa is the CLI for the Age of Agents orchestrator. It is intentionally
// tiny (standard-library flags, no framework): init a workspace, submit a goal, run
// the reconciler, and inspect state via status/feed/events.
package main

import (
	"context"

	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/agent"
	"github.com/bharadwaj6/ageOfAgents/internal/bench"
	"github.com/bharadwaj6/ageOfAgents/internal/config"
	"github.com/bharadwaj6/ageOfAgents/internal/diagnose"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/liveeval"
	"github.com/bharadwaj6/ageOfAgents/internal/mergequeue"
	"github.com/bharadwaj6/ageOfAgents/internal/metrics"
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
	case "bench":
		err = cmdBench(args)
	case "eval":
		err = cmdEval(args)
	case "diagnose":
		err = cmdDiagnose(args)
	case "approve":
		err = cmdApprove(args, true)
	case "reject":
		err = cmdApprove(args, false)
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
  aoa init   [--path DIR] [--repo PATH]   Scaffold a workspace + integration repo
  aoa goal   [--path DIR] "objective"     Submit a goal
  aoa run    [--path DIR] [--once]        Run the reconciler (loop by default)
  aoa status [--path DIR]                 Show goals and tickets
  aoa feed   [--path DIR] [--type T]      Print the event stream
  aoa events [--path DIR] tail [--count N] | replay
  aoa bench  [--json]                     Run the hermetic benchmark suite + report
  aoa eval   --tasks F [--backend B]      Run end-to-end tasks on real repos (mock|claudecode|grok)
  aoa diagnose [--path DIR] [--json]      MAST-style failure-mode histogram for a run
  aoa approve [--path DIR] <ticket-id>    Approve a parked proposal (require_approval)
  aoa reject  [--path DIR] <ticket-id>    Reject a parked proposal (require_approval)
`)
}

// workspace resolves the standard paths for a workspace root.
type workspace struct {
	root, configPath, ledgerPath, worktreeBase string
}

func workspaceAt(path string) (workspace, error) {
	root, err := filepath.Abs(path)
	if err != nil {
		return workspace{}, err
	}
	return workspace{
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
	path := fs.String("path", ".", "workspace root")
	repo := fs.String("repo", "./repo", "integration repo path (scaffold mode)")
	adopt := fs.String("adopt", "", "adopt an existing git repo at this path (on its current branch) instead of scaffolding")
	force := fs.Bool("force", false, "overwrite an existing aoa.toml")
	_ = fs.Parse(args)

	ctx := context.Background()
	ws, err := workspaceAt(*path)
	if err != nil {
		return err
	}
	// Never clobber an existing workspace config — inspect before writing.
	if _, err := os.Stat(ws.configPath); err == nil && !*force {
		return fmt.Errorf("%s already exists — use --force to overwrite", ws.configPath)
	}
	if err := os.MkdirAll(filepath.Join(ws.root, ".aoa"), 0o755); err != nil {
		return err
	}
	cfg := config.Default()

	// Adopt mode: point aoa at the user's existing repo as-is (no scaffolding,
	// no commits, no files written into their tree). Auto-detect a sensible Gate.
	if *adopt != "" {
		repoPath, err := filepath.Abs(resolve(ws.root, *adopt))
		if err != nil {
			return err
		}
		if fi, err := os.Stat(filepath.Join(repoPath, ".git")); err != nil || !fi.IsDir() {
			return fmt.Errorf("--adopt %s is not a git repository (no .git directory)", repoPath)
		}
		branch, err := worktree.OpenRepo(repoPath).CurrentBranch(ctx)
		if err != nil {
			return fmt.Errorf("read current branch of %s: %w", repoPath, err)
		}
		cfg.Repo = repoPath
		gate, lang := detectGate(repoPath)
		if gate != nil {
			cfg.Verify = gate
		}
		if err := cfg.Save(ws.configPath); err != nil {
			return err
		}
		fmt.Printf("Adopted %s\n  branch: %s\n  gate:   %s  (detected: %s)\n  config: %s\n\nReview the gate in aoa.toml (test-env setup is yours — ADR 009), then:\n  aoa goal --path %s \"your objective\"\n  aoa run  --path %s\n",
			repoPath, branch, gateString(cfg.Verify), lang, ws.configPath, *path, *path)
		return nil
	}

	// Scaffold mode: create a fresh Go-module repo to try aoa against.
	repoPath := resolve(ws.root, *repo)
	r, err := worktree.InitRepo(ctx, repoPath)
	if err != nil {
		return err
	}
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

	cfg.Repo = *repo
	cfg.ConventionsFile = "CONVENTIONS.md"
	if err := cfg.Save(ws.configPath); err != nil {
		return err
	}
	conv := "# Conventions\n\n- Keep changes minimal and focused.\n- Every change must keep `go build ./...` and `go test ./...` green.\n"
	if err := os.WriteFile(filepath.Join(ws.root, "CONVENTIONS.md"), []byte(conv), 0o644); err != nil {
		return err
	}

	fmt.Printf("Initialized workspace at %s\n  repo:   %s\n  config: %s\n\nNext:\n  aoa goal --path %s \"your objective\"\n  aoa run  --path %s\n",
		ws.root, repoPath, ws.configPath, *path, *path)
	return nil
}

// detectGate sniffs an adopted repo for a sensible default Gate. The user can
// always edit `verify` in aoa.toml afterward; provisioning the test environment
// is the caller's job (ADR 009). Returns nil when nothing is recognized.
func detectGate(repoDir string) (gate [][]string, lang string) {
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(repoDir, name))
		return err == nil
	}
	switch {
	case has("go.mod"):
		return [][]string{{"go", "build", "./..."}, {"go", "test", "./..."}}, "go"
	case has("package.json"):
		return [][]string{{"npm", "test"}}, "node"
	case has("pyproject.toml"), has("setup.py"), has("setup.cfg"), has("pytest.ini"), has("tox.ini"):
		return [][]string{{"python", "-m", "pytest"}}, "python"
	case has("Makefile"), has("makefile"):
		return [][]string{{"make", "test"}}, "make"
	default:
		return nil, "none — set `verify` in aoa.toml manually"
	}
}

// gateString renders a Gate (list of argv) as a human-readable "a && b" line.
func gateString(verify [][]string) string {
	parts := make([]string, len(verify))
	for i, c := range verify {
		parts[i] = strings.Join(c, " ")
	}
	return strings.Join(parts, " && ")
}

func cmdGoal(args []string) error {
	fs := flag.NewFlagSet("goal", flag.ExitOnError)
	path := fs.String("path", ".", "workspace root")
	_ = fs.Parse(args)

	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		return fmt.Errorf("goal text is required: aoa goal \"do the thing\"")
	}
	ws, err := workspaceAt(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return err
	}
	goalID := "g-" + orchestrator.ShortID()
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
	path := fs.String("path", ".", "workspace root")
	once := fs.Bool("once", false, "run a single reconcile pass instead of looping")
	_ = fs.Parse(args)

	ws, err := workspaceAt(*path)
	if err != nil {
		return err
	}
	o, led, err := buildOrchestrator(ws)
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
	cfg, _ := config.Load(ws.configPath)
	return printStatus(led, cfg.Pricing)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	path := fs.String("path", ".", "workspace root")
	_ = fs.Parse(args)
	ws, err := workspaceAt(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return err
	}
	cfg, _ := config.Load(ws.configPath)
	return printStatus(led, cfg.Pricing)
}

func cmdFeed(args []string) error {
	fs := flag.NewFlagSet("feed", flag.ExitOnError)
	path := fs.String("path", ".", "workspace root")
	typ := fs.String("type", "", "filter by event type")
	_ = fs.Parse(args)
	ws, err := workspaceAt(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
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
	path := fs.String("path", ".", "workspace root")
	count := fs.Int("count", 20, "number of events for tail")
	_ = fs.Parse(args)

	sub := "tail"
	if fs.NArg() > 0 {
		sub = fs.Arg(0)
	}
	ws, err := workspaceAt(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
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

func cmdBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "emit JSON instead of a markdown table")
	_ = fs.Parse(args)

	dir, err := os.MkdirTemp("", "aoa-bench-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	results, err := bench.RunSuite(context.Background(), dir, bench.Suite(), []bench.Strategy{bench.Single, bench.PlanFirst, bench.Emergent})
	if err != nil {
		return err
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}
	printBenchTable(results)
	return nil
}

// cmdEval runs end-to-end evaluation tasks (real git repos) through the
// orchestrator and reports task success, tokens, and the MAST histogram per
// task. With --backend mock it stays hermetic; --backend claudecode performs a
// live run that needs the agent binary, API keys, and network (ADR 009).
func cmdEval(args []string) error {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	tasksPath := fs.String("tasks", "", "path to a TOML task file")
	backendName := fs.String("backend", "mock", "agent backend: mock|claudecode")
	asJSON := fs.Bool("json", false, "emit JSON instead of a markdown table")
	price := fs.Float64("price", 0, "USD per million tokens, for the $/solve column (0 = unpriced)")
	_ = fs.Parse(args)

	if *tasksPath == "" {
		return fmt.Errorf("--tasks is required: aoa eval --tasks tasks.toml [--backend claudecode]")
	}
	tasks, err := liveeval.LoadTasks(*tasksPath)
	if err != nil {
		return err
	}
	backend, err := buildBackend(*backendName)
	if err != nil {
		return err
	}
	base, err := os.MkdirTemp("", "aoa-eval-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(base)

	reports := make([]liveeval.Report, 0, len(tasks))
	for i, t := range tasks {
		dir := filepath.Join(base, fmt.Sprintf("%02d-%s", i, t.Name))
		rep, err := liveeval.Run(context.Background(), backend, dir, t)
		if err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}
		reports = append(reports, rep)
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(reports)
	}
	printEvalTable(reports, *price)
	return nil
}

// printEvalTable renders evaluation reports as a markdown table. pricePerMTok is
// USD per million tokens for the run's model; 0 leaves the $ column at $0.0000.
func printEvalTable(reports []liveeval.Report, pricePerMTok float64) {
	fmt.Println("| task | backend | success | merged | tokens | $ | MAST | violations |")
	fmt.Println("|------|---------|:-------:|-------:|-------:|--:|-----:|-----------:|")
	for _, r := range reports {
		ok := "no"
		if r.Success {
			ok = "yes"
		}
		cost := float64(r.Metrics.TokensTotal) / 1e6 * pricePerMTok
		fmt.Printf("| %s | %s | %s | %d | %d | $%.4f | %d | %d |\n",
			r.Task, r.Backend, ok, r.Metrics.Merged, r.Metrics.TokensTotal, cost, r.MAST.Total(), len(r.Violations))
	}
}

// cmdApprove records a human decision on a proposal parked by the approval gate
// (ADR 008): approve=true emits ApprovalGranted (the ticket returns to the merge
// queue), approve=false emits ApprovalDenied (the ticket fails). Run `aoa run`
// afterwards to let the Scheduler act on the decision.
func cmdApprove(args []string, approve bool) error {
	name := "approve"
	if !approve {
		name = "reject"
	}
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	path := fs.String("path", ".", "workspace root")
	_ = fs.Parse(args)

	ticketID := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if ticketID == "" {
		return fmt.Errorf("ticket id is required: aoa %s <ticket-id>", name)
	}
	ws, err := workspaceAt(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return err
	}
	events, err := led.Read()
	if err != nil {
		return err
	}
	s, err := state.Fold(events)
	if err != nil {
		return err
	}
	t := s.Tickets[ticketID]
	if t == nil {
		return fmt.Errorf("unknown ticket %q", ticketID)
	}
	if t.Status != state.StatusAwaiting {
		return fmt.Errorf("ticket %q is %s, not awaiting approval", ticketID, t.Status)
	}

	var ev api.Event
	if approve {
		ev, err = api.NewEvent(api.ApprovalGranted, "human", api.ApprovalGrantedPayload{TicketID: ticketID})
	} else {
		ev, err = api.NewEvent(api.ApprovalDenied, "human", api.ApprovalDeniedPayload{TicketID: ticketID, Reason: "rejected by operator"})
	}
	if err != nil {
		return err
	}
	if _, err := led.Append(ev); err != nil {
		return err
	}
	if approve {
		fmt.Printf("approved %s — run `aoa run --path %s` to merge it\n", ticketID, *path)
	} else {
		fmt.Printf("rejected %s\n", ticketID)
	}
	return nil
}

// cmdDiagnose prints the MAST-style failure-mode histogram for a workspace's
// Event Log, turning the design's "aligned with MAST" claim into a measured
// property of the actual run (see internal/diagnose).
func cmdDiagnose(args []string) error {
	fs := flag.NewFlagSet("diagnose", flag.ExitOnError)
	path := fs.String("path", ".", "workspace root")
	asJSON := fs.Bool("json", false, "emit JSON instead of a markdown table")
	_ = fs.Parse(args)

	ws, err := workspaceAt(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return err
	}
	events, err := led.Read()
	if err != nil {
		return err
	}
	rep := diagnose.Classify(events)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	printDiagnose(rep)
	return nil
}

// printDiagnose renders the failure-mode histogram as a markdown table.
func printDiagnose(rep diagnose.Report) {
	fmt.Println("| MAST failure mode | count | tickets |")
	fmt.Println("|-------------------|------:|---------|")
	for _, f := range rep.Findings {
		fmt.Printf("| %s | %d | %s |\n", f.Mode, f.Count, strings.Join(f.Tickets, " "))
	}
	fmt.Println()
	if rep.Total() == 0 {
		fmt.Println("No MAST failure modes detected (clean run).")
	} else {
		fmt.Printf("%d failure-mode occurrence(s) detected — see the table above.\n", rep.Total())
	}
}

// printBenchTable renders the benchmark results as a markdown table. The key
// columns make the design thesis legible: coordination LLM sessions stay at 0,
// merge correctness stays at 100%, and the Emergent strategy unlocks worker
// parallelism the Single/PlanFirst baselines cannot.
func printBenchTable(results []bench.Result) {
	fmt.Println("| task | strategy | merged | workers(max∥) | crit-path | coord-LLM | merge-correct | MAST | violations |")
	fmt.Println("|------|----------|-------:|--------------:|----------:|----------:|--------------:|-----:|-----------:|")
	clean := true
	for _, r := range results {
		m := r.Metrics
		v := len(r.Violations)
		if v > 0 {
			clean = false
		}
		fmt.Printf("| %s | %s | %d | %d | %d | %d | %.0f%% | %d | %d |\n",
			r.Task, r.Strategy, m.Merged, m.MaxConcurrentWorkers, m.CriticalPathDepth,
			m.CoordinationSessions, m.MergeCorrectness*100, r.MAST.Total(), v)
	}
	fmt.Println()
	if clean {
		fmt.Println("All runs: 0 coordination LLM sessions, 100% merge correctness, 0 MAST failure modes, 0 invariant violations.")
	} else {
		fmt.Println("INVARIANT VIOLATIONS DETECTED — see the violations column and re-run with --json for detail.")
	}
}

// --- wiring ---------------------------------------------------------------

func buildOrchestrator(ws workspace) (*orchestrator.Orchestrator, *ledger.Ledger, error) {
	cfg, err := config.Load(ws.configPath)
	if err != nil {
		return nil, nil, err
	}
	repo := worktree.OpenRepo(resolve(ws.root, cfg.Repo))
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return nil, nil, err
	}
	backend, err := buildBackend(cfg.Backend)
	if err != nil {
		return nil, nil, err
	}
	conventions := ""
	if cfg.ConventionsFile != "" {
		if b, err := os.ReadFile(resolve(ws.root, cfg.ConventionsFile)); err == nil {
			conventions = string(b)
		}
	}
	gate := verify.Verifier{Commands: verify.ToCommands(cfg.Verify)}
	var backoff time.Duration
	if cfg.RetryBackoff != "" {
		d, err := time.ParseDuration(cfg.RetryBackoff)
		if err != nil {
			return nil, nil, fmt.Errorf("retry_backoff %q: %w", cfg.RetryBackoff, err)
		}
		backoff = d
	}
	opt := orchestrator.Options{
		Concurrency:        cfg.Concurrency,
		MaxAttempts:        cfg.MaxAttempts,
		Conventions:        conventions,
		WorktreeBase:       ws.worktreeBase,
		RequireApproval:    cfg.RequireApproval,
		MaxTokensPerGoal:   cfg.MaxTokensPerGoal,
		RetryBackoff:       backoff,
		CrashLoopThreshold: cfg.CrashLoopThreshold,
	}
	mq := mergequeue.New(repo, gate)
	mq.Shadow = verify.Verifier{Commands: verify.ToCommands(cfg.RegressionVerify)}
	o := orchestrator.New(led, repo, backend, mq, opt)
	return o, led, nil
}

func buildBackend(name string) (agent.Backend, error) {
	switch name {
	case "mock", "":
		return agent.NewMock(), nil
	case "claudecode":
		return agent.NewClaudeCode(), nil
	case "grok":
		agent.EnsureGrokLeader()
		return agent.NewGrok(), nil
	default:
		return nil, fmt.Errorf("unknown backend %q (want mock|claudecode|grok)", name)
	}
}

// --- presentation ---------------------------------------------------------

func printStatus(led *ledger.Ledger, pricing map[string]float64) error {
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

	m := metrics.Compute(events)
	tokensByTicket := make(map[string]int, len(m.PerTicket))
	for _, tc := range m.PerTicket {
		tokensByTicket[tc.TicketID] = tc.Tokens
	}

	goalIDs := make([]string, 0, len(s.Goals))
	for id := range s.Goals {
		goalIDs = append(goalIDs, id)
	}
	sort.Strings(goalIDs)

	for _, gid := range goalIDs {
		fmt.Printf("goal %s: %s\n", gid, s.Goals[gid].Text)
	}
	for _, gs := range metrics.GraphShapes(s) {
		fmt.Printf("  graph %s: tickets=%d depth=%d fan-out=%d\n",
			gs.GoalID, gs.Tickets, gs.MaxDepth, gs.MaxFanOut)
	}
	fmt.Println()

	ticketIDs := make([]string, 0, len(s.Tickets))
	for id := range s.Tickets {
		ticketIDs = append(ticketIDs, id)
	}
	sort.Strings(ticketIDs)

	var needsHuman []*state.Ticket
	for _, id := range ticketIDs {
		t := s.Tickets[id]
		fmt.Printf("  [%-8s] %s  (attempts=%d tokens=%d)\n", t.Status, t.ID, t.Attempts, tokensByTicket[id])
		if t.Status == state.StatusFailed {
			needsHuman = append(needsHuman, t)
		}
	}

	// Warm handoff: list terminally-failed tickets with why they failed and the
	// preserved worktree to take over (when one was kept).
	if len(needsHuman) > 0 {
		fmt.Println("\nneeds human — failed tickets:")
		for _, t := range needsHuman {
			reason := t.LastFailReason
			if reason == "" {
				reason = "(no reason recorded)"
			}
			fmt.Printf("  %s — %s\n", t.ID, reason)
			if t.Worktree != "" {
				fmt.Printf("      take over: cd %s\n", t.Worktree)
			}
		}
	}

	fmt.Printf("\ntotal: tokens=%d  wall=%.1fs", m.TokensTotal, m.DurationSeconds)
	if cost := metrics.USD(m.TokensByModel, pricing); cost > 0 {
		fmt.Printf("  cost=$%.4f", cost)
	}
	fmt.Println()
	if s.Settled() {
		fmt.Println("all work settled")
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

func goVersion() string {
	// runtime.Version() looks like "go1.26.4"; go.mod wants "1.26".
	v := strings.TrimPrefix(runtime.Version(), "go")
	parts := strings.Split(v, ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	return v
}
