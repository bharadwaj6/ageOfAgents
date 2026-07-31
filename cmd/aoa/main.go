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
	"github.com/bharadwaj6/ageOfAgents/internal/otel"
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
	case "amend":
		err = cmdAmend(args)
	case "run":
		err = cmdRun(args)
	case "compact":
		err = cmdCompact(args)
	case "status":
		err = cmdStatus(args)
	case "feed":
		err = cmdFeed(args)
	case "events":
		err = cmdEvents(args)
	case "bench":
		err = cmdBench(args)
	case "serve":
		err = cmdServe(args)
	case "eval":
		err = cmdEval(args)
	case "diagnose":
		err = cmdDiagnose(args)
	case "otel":
		err = cmdOtel(args)
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
  aoa amend  [--path DIR] <goal-id> "..."  Append steering guidance to a goal mid-run
  aoa run    [--path DIR] [--once] [--otel | --otel-live]  Run the reconciler (loop by default)
  aoa compact [--path DIR]                Compact the event log to a single state snapshot
  aoa status [--path DIR] [--watch]       Show goals and tickets (--watch to live-refresh)
  aoa events [--path DIR] tail [--count N] [--type T] | replay [--type T]
  aoa feed   [--path DIR] [--type T]      Deprecated alias for 'events tail'
  aoa bench  [--json]                     Run the hermetic benchmark suite + report
  aoa serve  [--path DIR] [--port N]      Run a GitHub webhook server
  aoa eval   --tasks F [--backend B] [--price-file F] [--max-cost $] [--otel]
                                          Run end-to-end tasks on real repos (mock|claudecode|grok)
  aoa diagnose [--path DIR] [--json]      MAST-style failure-mode histogram for a run
  aoa otel export [--path DIR]            Replay the Event Log to OTLP traces + metrics
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

func loadWorkspace(path string) (workspace, *ledger.Ledger, error) {
	ws, err := workspaceAt(path)
	if err != nil {
		return ws, nil, err
	}
	led, err := ledger.Open(ws.ledgerPath)
	return ws, led, err
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

// cmdAmend appends steering guidance to a Goal mid-run (GoalAmended). Future
// dispatches and retries carry the amended context; a running worker finishes
// its current attempt uninterrupted. Run `aoa run` afterward to act on it.
func cmdAmend(args []string) error {
	fs := flag.NewFlagSet("amend", flag.ExitOnError)
	path := fs.String("path", ".", "workspace root")
	_ = fs.Parse(args)

	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("usage: aoa amend <goal-id> \"new guidance\"")
	}
	goalID := rest[0]
	guidance := strings.TrimSpace(strings.Join(rest[1:], " "))
	if guidance == "" {
		return fmt.Errorf("amendment guidance is required: aoa amend %s \"...\"", goalID)
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
	if s.Goals[goalID] == nil {
		return fmt.Errorf("unknown goal %q", goalID)
	}
	ev, err := api.NewEvent(api.GoalAmended, "human", api.GoalAmendedPayload{GoalID: goalID, Guidance: guidance})
	if err != nil {
		return err
	}
	if _, err := led.Append(ev); err != nil {
		return err
	}
	fmt.Printf("amended goal %s — run `aoa run --path %s` to apply it to pending work\n", goalID, *path)
	return nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	path := fs.String("path", ".", "workspace root")
	once := fs.Bool("once", false, "run a single reconcile pass instead of looping")
	otelExport := fs.Bool("otel", false, "after the run, replay the Event Log to OTLP (needs OTEL_EXPORTER_OTLP_ENDPOINT)")
	otelLive := fs.Bool("otel-live", false, "stream spans to OTLP live as events happen (instead of one post-hoc export)")
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

	// Live streaming: open spans for any in-flight work, then have the ledger feed
	// each new event to the emitter as it's appended (off unless an endpoint is set).
	var live *otel.Live
	if *otelLive {
		if !otel.Enabled() {
			fmt.Fprintln(os.Stderr, "note: --otel-live set but OTEL_EXPORTER_OTLP_ENDPOINT is unset; skipping")
		} else if live, err = otel.NewLive(ctx); err != nil {
			return err
		} else {
			if seed, rerr := led.Read(); rerr == nil {
				live.Seed(seed)
			}
			led.SetAppendHook(live.Observe)
		}
	}

	if *once {
		err = o.ReconcileOnce(ctx)
	} else {
		err = o.Run(ctx)
	}
	if live != nil {
		led.SetAppendHook(nil)
		if serr := live.Shutdown(ctx); serr != nil && err == nil {
			err = serr
		}
	}
	if err != nil {
		return err
	}

	cfg, _ := config.Load(ws.configPath)
	if *otelExport {
		if err := exportOTel(ws, led, cfg); err != nil {
			return err
		}
	}
	_, err = printStatus(led, cfg.Pricing)
	return err
}

func cmdCompact(args []string) error {
	fs := flag.NewFlagSet("compact", flag.ExitOnError)
	path := fs.String("path", ".", "Path to the workspace")
	fs.Parse(args)

	_, led, err := loadWorkspace(*path)
	if err != nil {
		return err
	}

	events, err := led.Read()
	if err != nil {
		return fmt.Errorf("read ledger: %w", err)
	}
	if len(events) == 0 {
		fmt.Println("Ledger is empty")
		return nil
	}

	// Fast path: if the last event is already a snapshot, and there is only 1 event, we are done.
	if len(events) == 1 && events[0].Type == api.StateSnapshot {
		fmt.Printf("Ledger already compacted up to sequence %d\n", events[0].Seq)
		return nil
	}

	st, err := state.Fold(events)
	if err != nil {
		return fmt.Errorf("fold state: %w", err)
	}

	rawState, err := json.Marshal(st)
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	snapEvent, err := api.NewEvent(api.StateSnapshot, "system", api.StateSnapshotPayload{State: rawState})
	if err != nil {
		return err
	}
	snapEvent.Seq = st.LastSeq

	if err := led.Compact(snapEvent); err != nil {
		return err
	}

	fmt.Printf("Compacted ledger up to sequence %d (compressed %d events -> 1)\n", st.LastSeq, len(events))
	return nil
}

// exportOTel replays the workspace Event Log into OTLP traces + metrics. It is a
// no-op (with a stderr hint) when no OTLP endpoint is configured, so `--otel` on
// an offline run never fails. The whole projection lives in internal/otel.
func exportOTel(ws workspace, led *ledger.Ledger, cfg config.Config) error {
	if !otel.Enabled() {
		fmt.Fprintln(os.Stderr, "note: --otel set but OTEL_EXPORTER_OTLP_ENDPOINT is unset; skipping export")
		return nil
	}
	events, err := led.Read()
	if err != nil {
		return err
	}
	if err := otel.Export(context.Background(), events, metrics.Compute(events), diagnose.Classify(events), cfg.Pricing); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "exported traces + metrics via OTLP")
	return nil
}

// cmdOtel replays a finished run's Event Log to an OTLP backend on demand —
// the same projection `aoa run --otel` does, but as a standalone step you can
// run against any existing workspace (e.g. after a bench/eval).
func cmdOtel(args []string) error {
	sub := "export"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	if sub != "export" {
		return fmt.Errorf("unknown otel subcommand %q (want: export)", sub)
	}
	fs := flag.NewFlagSet("otel", flag.ExitOnError)
	path := fs.String("path", ".", "workspace root")
	_ = fs.Parse(args)
	if !otel.Enabled() {
		return fmt.Errorf("no OTLP endpoint configured — set OTEL_EXPORTER_OTLP_ENDPOINT (see docs/integrations/honeycomb.md)")
	}
	ws, err := workspaceAt(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return err
	}
	cfg, _ := config.Load(ws.configPath)
	return exportOTel(ws, led, cfg)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	path := fs.String("path", ".", "workspace root")
	watch := fs.Bool("watch", false, "re-render until all work settles (poll the Event Log)")
	interval := fs.Duration("interval", 2*time.Second, "refresh interval for --watch")
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

	if !*watch {
		_, err = printStatus(led, cfg.Pricing)
		return err
	}
	// Watch mode: clear + re-render each interval until settled. No daemon — just
	// a poll loop the user can Ctrl-C out of.
	for {
		fmt.Print("\033[H\033[2J") // clear screen, cursor home
		fmt.Printf("aoa status — %s  (Ctrl-C to stop)\n\n", time.Now().Format("15:04:05"))
		settled, err := printStatus(led, cfg.Pricing)
		if err != nil {
			return err
		}
		if settled {
			return nil
		}
		time.Sleep(*interval)
	}
}

// cmdFeed is a deprecated alias for `aoa events tail`. Kept so existing scripts
// don't break; it prints the whole stream (the old behavior) and points at the
// canonical command.
func cmdFeed(args []string) error {
	fmt.Fprintln(os.Stderr, "note: `aoa feed` is deprecated — use `aoa events tail [--type T]`")
	return cmdEvents(append([]string{"tail", "--count", "0"}, args...))
}

func cmdEvents(args []string) error {
	fs := flag.NewFlagSet("events", flag.ExitOnError)
	path := fs.String("path", ".", "workspace root")
	count := fs.Int("count", 20, "number of events for tail (0 = all)")
	typ := fs.String("type", "", "filter by event type")
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
		renderEvents(filterEvents(events, *typ), *count, false)
	case "replay":
		renderEvents(filterEvents(events, *typ), 0, true)
	default:
		return fmt.Errorf("unknown events subcommand %q (want tail|replay)", sub)
	}
	return nil
}

// filterEvents keeps only events of the given type ("" = all).
func filterEvents(events []api.Event, typ string) []api.Event {
	if typ == "" {
		return events
	}
	out := make([]api.Event, 0, len(events))
	for _, e := range events {
		if string(e.Type) == typ {
			out = append(out, e)
		}
	}
	return out
}

// renderEvents prints events; count>0 keeps only the last count (a tail),
// withPayload appends the raw JSON payload (replay).
func renderEvents(events []api.Event, count int, withPayload bool) {
	if count > 0 && len(events) > count {
		events = events[len(events)-count:]
	}
	for _, e := range events {
		if withPayload {
			fmt.Printf("%s  %s\n", formatEvent(e), string(e.Payload))
		} else {
			fmt.Println(formatEvent(e))
		}
	}
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
	price := fs.Float64("price", 0, "flat USD per million tokens for the $ column (0 = unpriced)")
	priceFile := fs.String("price-file", "", "TOML [pricing] file (model -> USD/Mtok) for per-model cost")
	maxCost := fs.Float64("max-cost", 0, "stop launching tasks once cumulative $ crosses this ceiling (0 = no cap)")
	otelExport := fs.Bool("otel", false, "export each task's Event Log to OTLP (needs OTEL_EXPORTER_OTLP_ENDPOINT)")
	_ = fs.Parse(args)

	if *tasksPath == "" {
		return fmt.Errorf("--tasks is required: aoa eval --tasks tasks.toml [--backend claudecode]")
	}
	tasks, err := liveeval.LoadTasks(*tasksPath)
	if err != nil {
		return err
	}
	var priceMap map[string]float64
	if *priceFile != "" {
		if priceMap, err = config.LoadPricing(*priceFile); err != nil {
			return err
		}
	}
	cfg, _ := config.Load(config.FileName)
	cfg.Backend = *backendName // override with flag if provided
	backend, err := buildBackend(cfg)
	if err != nil {
		return err
	}
	base, err := os.MkdirTemp("", "aoa-eval-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(base)

	ctx := context.Background()
	reports := make([]liveeval.Report, 0, len(tasks))
	var spent float64
	skipped := 0
	for i, t := range tasks {
		// Cost ceiling is checked *between* tasks: finish what's running, then stop
		// launching once cumulative spend crosses --max-cost.
		if *maxCost > 0 && spent >= *maxCost {
			skipped = len(tasks) - i
			fmt.Fprintf(os.Stderr, "max-cost $%.4f reached after $%.4f; skipping %d remaining task(s)\n", *maxCost, spent, skipped)
			break
		}
		dir := filepath.Join(base, fmt.Sprintf("%02d-%s", i, t.Name))
		rep, err := liveeval.Run(ctx, backend, dir, t)
		if err != nil {
			return fmt.Errorf("%s: %w", t.Name, err)
		}
		reports = append(reports, rep)
		spent += evalCost(rep.Metrics, priceMap, *price)

		if *otelExport && otel.Enabled() {
			if led, lerr := ledger.Open(filepath.Join(dir, "events.jsonl")); lerr == nil {
				if evs, rerr := led.Read(); rerr == nil {
					if eerr := otel.ExportTask(ctx, evs, rep.Metrics, rep.MAST, priceMap, t.Name); eerr != nil {
						fmt.Fprintf(os.Stderr, "otel export %s: %v\n", t.Name, eerr)
					}
				}
			}
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(reports)
	}
	printEvalTable(reports, priceMap, *price, skipped)
	return nil
}

// evalCost prices one task's run: per-model when a price map is given, else a
// flat rate over total tokens. An unpriced run reports $0.
func evalCost(m metrics.Metrics, priceMap map[string]float64, flat float64) float64 {
	if len(priceMap) > 0 {
		return metrics.USD(m.TokensByModel, priceMap)
	}
	return float64(m.TokensTotal) / 1e6 * flat
}

// printEvalTable renders evaluation reports as a markdown table with a per-task $
// column and an aggregate footer (tokens, $, solve rate, tasks run vs skipped).
func printEvalTable(reports []liveeval.Report, priceMap map[string]float64, flat float64, skipped int) {
	fmt.Println("| task | backend | success | merged | tokens | $ | MAST | violations |")
	fmt.Println("|------|---------|:-------:|-------:|-------:|--:|-----:|-----------:|")
	var totTokens, solved int
	var totCost float64
	for _, r := range reports {
		ok := "no"
		if r.Success {
			ok = "yes"
			solved++
		}
		cost := evalCost(r.Metrics, priceMap, flat)
		totTokens += r.Metrics.TokensTotal
		totCost += cost
		fmt.Printf("| %s | %s | %s | %d | %d | $%.4f | %d | %d |\n",
			r.Task, r.Backend, ok, r.Metrics.Merged, r.Metrics.TokensTotal, cost, r.MAST.Total(), len(r.Violations))
	}
	ran := len(reports)
	total := ran + skipped
	fmt.Printf("\ntotal: solved=%d/%d  tokens=%d  cost=$%.4f  (ran %d/%d", solved, ran, totTokens, totCost, ran, total)
	if skipped > 0 {
		fmt.Printf(", skipped %d by --max-cost", skipped)
	}
	fmt.Println(")")
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
	backend, err := buildBackend(cfg)
	if err != nil {
		return nil, nil, err
	}
	conventions := ""
	if cfg.ConventionsFile != "" {
		if b, err := os.ReadFile(resolve(ws.root, cfg.ConventionsFile)); err == nil {
			conventions = string(b)
		}
	}
	gate := verify.Verifier{Commands: verify.ToCommands(cfg.Verify), Sandbox: cfg.Sandbox}
	var backoff time.Duration
	if cfg.RetryBackoff != "" {
		d, err := time.ParseDuration(cfg.RetryBackoff)
		if err != nil {
			return nil, nil, fmt.Errorf("retry_backoff %q: %w", cfg.RetryBackoff, err)
		}
		backoff = d
	}
	var stall time.Duration
	if cfg.StallTimeout != "" {
		d, err := time.ParseDuration(cfg.StallTimeout)
		if err != nil {
			return nil, nil, fmt.Errorf("stall_timeout %q: %w", cfg.StallTimeout, err)
		}
		stall = d
	}
	opt := orchestrator.Options{
		Concurrency:        cfg.Concurrency,
		MaxAttempts:        cfg.MaxAttempts,
		BestOfN:            cfg.BestOfN,
		Conventions:        conventions,
		WorktreeBase:       ws.worktreeBase,
		RequireApproval:    cfg.RequireApproval,
		MaxTokensPerGoal:   cfg.MaxTokensPerGoal,
		MaxUsdPerGoal:      cfg.MaxUsdPerGoal,
		Pricing:            cfg.Pricing,
		RetryBackoff:       backoff,
		CrashLoopThreshold: cfg.CrashLoopThreshold,
		// Termination gates; zero means "Scheduler default" (orchestrator.New).
		StallTimeout:      stall,
		MaxPasses:         cfg.MaxPasses,
		MaxGraphDepth:     cfg.MaxGraphDepth,
		MaxTicketsPerGoal: cfg.MaxTicketsPerGoal,
		MaxFanOut:         cfg.MaxFanOut,
	}
	mq := mergequeue.New(repo, gate)
	if len(cfg.RegressionVerify) > 0 {
		mq.Shadow = verify.Verifier{Commands: verify.ToCommands(cfg.RegressionVerify), Sandbox: cfg.Sandbox}
	}
	o := orchestrator.New(led, repo, backend, mq, opt)
	return o, led, nil
}

func buildBackendSingle(name string, cfg config.Config) (agent.Backend, error) {
	if bCfg, ok := cfg.Backends[name]; ok {
		switch bCfg.Type {
		case "openai_compatible":
			return agent.NewOpenAICompatible(name, bCfg.Model, bCfg.BaseURL, bCfg.APIKeyEnv), nil
		default:
			return nil, fmt.Errorf("unknown plugin type %q for backend %q", bCfg.Type, name)
		}
	}

	switch name {
	case "mock", "":
		return agent.NewMock(), nil
	case "claudecode":
		return agent.NewClaudeCode(), nil
	case "grok":
		agent.EnsureGrokLeader()
		return agent.NewGrok(), nil
	case "openai":
		return agent.NewOpenAI(), nil
	case "anthropic":
		return agent.NewAnthropic(), nil
	default:
		return nil, fmt.Errorf("unknown backend %q (want mock|claudecode|grok|openai|anthropic or a configured plugin)", name)
	}
}

func buildBackend(cfg config.Config) (agent.Backend, error) {
	primary, err := buildBackendSingle(cfg.Backend, cfg)
	if err != nil {
		return nil, err
	}
	if len(cfg.FallbackBackends) == 0 {
		return primary, nil
	}
	backends := []agent.Backend{primary}
	for _, fb := range cfg.FallbackBackends {
		b, err := buildBackendSingle(fb, cfg)
		if err != nil {
			return nil, fmt.Errorf("invalid fallback backend %q: %w", fb, err)
		}
		backends = append(backends, b)
	}
	return agent.NewFallbackBackend(backends...), nil
}

// --- presentation ---------------------------------------------------------

// printStatus renders the run's live state and reports whether all work has
// settled (the signal --watch uses to stop polling).
func printStatus(led *ledger.Ledger, pricing map[string]float64) (settled bool, err error) {
	events, err := led.Read()
	if err != nil {
		return false, err
	}
	s, err := state.Fold(events)
	if err != nil {
		return false, err
	}
	if len(s.Goals) == 0 {
		fmt.Println("no goals submitted")
		return true, nil
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
	if m.MergeQueueMaxDepth > 0 {
		fmt.Printf("merge queue: max-depth=%d  wait-mean=%.1fs  wait-max=%.1fs\n",
			m.MergeQueueMaxDepth, m.MergeQueueWaitMean, m.MergeQueueWaitMax)
	}
	settled = s.Settled()
	if settled {
		fmt.Println("all work settled")
	}
	return settled, nil
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
