// Command aoa is the CLI for the Age of Agents orchestrator. It is intentionally
// tiny (standard-library flags, no framework): init a workspace, submit a goal, run
// the reconciler, and inspect state via status/feed/events.
package main

import (
	"bufio"
	"context"

	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"syscall"
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
	case "version", "--version", "-v":
		fmt.Println(versionString())
		return
	case "doctor":
		err = cmdDoctor(args)
	case "quickstart":
		err = cmdQuickstart(args)
	case "completion":
		err = cmdCompletion(args)
	case "init":
		err = cmdInit(args)
	case "goal":
		err = cmdGoal(args)
	case "amend":
		err = cmdAmend(args)
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
	case "cancel":
		err = cmdCancel(args)
	case "wait":
		err = cmdWait(args)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(exitCode(err))
	}
}

// exitError is an error that ends the process with a specific status instead of
// the default 1, so a caller scripting aoa can tell kinds of failure apart.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// exitCode is the process status for a command that failed with err: the code
// of the first exitError in its chain, else 1.
func exitCode(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return 1
}

func usage() {
	fmt.Print(`aoa - Age of Agents orchestrator

Usage:
  aoa quickstart [--path DIR]             Scaffold, submit a goal and run it — offline, one command
  aoa doctor [--path DIR]                 Check this workspace can actually run
  aoa init   [--path DIR] [--repo PATH | --adopt PATH] [--force]
                                          Scaffold a workspace, or adopt an existing repo
  aoa goal   [--path DIR] [--json] [--key K] [--source S] [--ref R] [--by B] "objective"
                                          Submit a goal (a repeated --key returns the existing one)
  aoa amend  [--path DIR] [--json] <goal-id> "..."
                                          Append steering guidance to a goal mid-run
  aoa run    [--path DIR] [--once | --interval D] [--otel | --otel-live]
                                          Run the reconciler (to settled by default)
  aoa status [--path DIR] [--watch] [--interval D]
                                          Show goals and tickets (--watch to live-refresh)
  aoa events [--path DIR] [tail [--count N] | replay] [--type T] [--json] [--since N] [--follow]
                                          Print the Event Log, or stream it (--json --since N --follow)
  aoa feed   [--path DIR] [--type T]      Deprecated alias for 'events tail'
  aoa bench  [--json]                     Run the hermetic benchmark suite + report
  aoa serve  [--path DIR] [--port N] [--secret S] [--allow LIST]
                                          Run a GitHub webhook server (always set --secret)
  aoa eval   --tasks F [--backend B] [--price P | --price-file F] [--max-cost $] [--json] [--otel]
                                          Run end-to-end tasks on real repos (any backend value)
  aoa diagnose [--path DIR] [--json]      MAST-style failure-mode histogram for a run
  aoa otel export [--path DIR]            Replay the Event Log to OTLP traces + metrics
  aoa approve [--path DIR] [--json] [--by B] [--reason R] <ticket-id>
                                          Approve a parked proposal (require_approval)
  aoa reject  [--path DIR] [--json] [--by B] [--reason R] <ticket-id>
                                          Reject a parked proposal (require_approval)
  aoa cancel  [--path DIR] [--json] [--by B] [--reason R] <goal-id>
                                          Withdraw a goal so none of its work lands
  aoa wait    [--path DIR] [--json] [--timeout D] <goal-id>...
                                          Block until each goal is complete; exit with its outcome
  aoa version                             Print the build version
  aoa completion bash|zsh|fish            Print a shell completion script

New here? One command, entirely offline, about ten seconds:
  aoa quickstart --path ./workspace
`)
}

// Build metadata. version is overwritten at release time via -ldflags; a binary
// built any other way reports "dev" rather than claiming a version it isn't.
var (
	version = "dev"
	commit  = ""
	date    = ""
)

type buildMeta struct {
	version, commit, date string
}

// versionString renders the build for `aoa version`. Without this every
// published binary was unable to say which build it was.
func versionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		info = nil
	}
	return formatVersion(resolveBuild(version, commit, date, info))
}

// resolveBuild prefers GoReleaser ldflags. When those still say "dev" (go
// install, a plain `go build`), it fills in module version and VCS metadata
// from *debug.BuildInfo so `aoa version` matches `go version -m`.
func resolveBuild(ldVersion, ldCommit, ldDate string, info *debug.BuildInfo) buildMeta {
	m := buildMeta{version: ldVersion, commit: ldCommit, date: ldDate}
	if m.version != "dev" || info == nil {
		return m
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		m.version = strings.TrimPrefix(v, "v")
	}
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			if m.commit == "" && s.Value != "" {
				rev := s.Value
				if len(rev) > 7 {
					rev = rev[:7]
				}
				m.commit = rev
			}
		case "vcs.time":
			if m.date == "" && s.Value != "" {
				m.date = s.Value
			}
		}
	}
	return m
}

func formatVersion(m buildMeta) string {
	s := "aoa " + m.version
	if m.commit != "" {
		s += " (" + m.commit
		if m.date != "" {
			s += ", " + m.date
		}
		s += ")"
	}
	return s
}

// workspace resolves the standard paths for a workspace root.
type workspace struct {
	root, configPath, ledgerPath, worktreeBase string
}

// describe attaches a description and usage example to a subcommand's flag set,
// so `aoa <cmd> --help` explains what the command does instead of dumping bare
// flag names. Go's flag package prints only the flags by default, which meant
// `aoa goal --help` never revealed that goal takes positional text at all.
func describe(fs *flag.FlagSet, summary, example string) {
	fs.Usage = func() {
		out := fs.Output()
		fmt.Fprintf(out, "%s\n\n", summary)
		if example != "" {
			fmt.Fprintf(out, "Example:\n  %s\n\n", example)
		}
		fmt.Fprintf(out, "Flags:\n")
		fs.PrintDefaults()
	}
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

// openWorkspace resolves a path that must already be a workspace. ledger.Open
// MkdirAll's its parent unconditionally, so without this check a mistyped
// --path quietly minted <typo>/.aoa/ and printed "no goals submitted" rather
// than saying that directory isn't a workspace. `aoa init` uses workspaceAt
// directly — it is the one command that legitimately runs before any config.
func openWorkspace(path string) (workspace, error) {
	ws, err := workspaceAt(path)
	if err != nil {
		return ws, err
	}
	if _, err := os.Stat(ws.configPath); err != nil {
		if os.IsNotExist(err) {
			return workspace{}, fmt.Errorf("%s is not an aoa workspace (no %s) — run `aoa init --path %s` first", ws.root, config.FileName, path)
		}
		return workspace{}, err
	}
	return ws, nil
}

func resolve(root, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(root, p)
}

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	describe(fs, "aoa init \u2014 scaffold a new workspace, or adopt an existing git repo.\n\nA workspace holds the Event Log (.aoa/) and aoa.toml. --repo scaffolds a fresh\ndemo repository to try things offline; --adopt points at a repo you already\nhave, on whatever branch it is on, and auto-detects its build/test Gate.", "aoa init --path ./workspace --adopt /path/to/your/repo")
	path := fs.String("path", ".", "workspace root")
	repo := fs.String("repo", "./repo", "integration repo path (scaffold mode)")
	adopt := fs.String("adopt", "", "adopt an existing git repo at this path (on its current branch) instead of scaffolding")
	force := fs.Bool("force", false, "overwrite an existing aoa.toml")
	_ = fs.Parse(args)

	ctx := context.Background()
	ws, err := workspaceAt(*path) // init is the one command that predates the config
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

// rejectStrayFlags fails on a flag that landed after the positional text.
// Go's flag package stops parsing at the first non-flag argument, so
// `aoa goal "fix the parser" --path ./ws` used to submit the literal goal
// "fix the parser --path ./ws" to the default workspace — no error, no warning,
// and every doc example puts --path first, which hid it.
func rejectStrayFlags(args []string) error {
	for _, a := range args {
		if strings.HasPrefix(a, "-") && a != "-" {
			return fmt.Errorf("flag %q came after the text; put flags first: aoa <cmd> [flags] \"text\"", a)
		}
	}
	return nil
}

func cmdGoal(args []string) error {
	fs := flag.NewFlagSet("goal", flag.ExitOnError)
	describe(fs, "aoa goal \u2014 submit an objective in plain English.\n\nThe Goal becomes one Task. Nothing is dispatched until you run `aoa run`.\nFlags must come before the text. With --key, submitting the same key again\nappends nothing and reports the Goal already submitted.", "aoa goal --path ./workspace \"add a greeting function\"")
	path := fs.String("path", ".", "workspace root")
	asJSON := fs.Bool("json", false, "print the result as one JSON line (pkg/api SubmitResult)")
	source := fs.String("source", "human", "entry point submitting the goal (e.g. a front door's name)")
	ref := fs.String("ref", "", "origin of the goal: a URL or tracker:id")
	key := fs.String("key", "", "idempotency key: re-submitting it returns the existing goal")
	by := fs.String("by", "", "who is submitting the goal")
	if err := fs.Parse(args); err != nil {
		return err
	}

	text := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if text == "" {
		return fmt.Errorf("goal text is required: aoa goal \"do the thing\"")
	}
	if err := rejectStrayFlags(fs.Args()); err != nil {
		return err
	}
	ws, err := openWorkspace(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return err
	}
	res, err := submitGoal(led, goalRequest{Text: text, Source: *source, Ref: *ref, Key: *key, By: *by})
	if err != nil {
		return err
	}
	switch {
	case *asJSON:
		return printJSON(res)
	case res.Duplicate:
		fmt.Printf("goal %s already submitted (key %q)\n", res.GoalID, *key)
	default:
		fmt.Printf("submitted goal %s: %q\n", res.GoalID, text)
	}
	return nil
}

// newGoalID returns a fresh random Goal id. A variable so a test can force a
// collision.
var newGoalID = func() string { return "g-" + orchestrator.ShortID() }

// goalRequest is one Goal to submit. Source names the entry point that produced
// it and becomes the event's actor. Ref points at its origin (a URL or
// "tracker:id") and By names who asked; both are recorded as given. Key, when
// non-empty, is an idempotency key: submitting it again returns the Goal it
// already names instead of creating another.
type goalRequest struct{ Text, Source, Ref, Key, By string }

// submitGoal appends a GoalSubmitted event and reports the Goal's id and the
// event's seq. A keyed request whose key is already on the log appends nothing
// and reports the existing Goal as a duplicate, with the seq of its original
// GoalSubmitted — so a poller that re-submits every cycle, or a redelivered
// webhook, costs one read instead of one event per attempt.
//
// The check and the append are one ledger transaction (Ledger.Update), so
// concurrent submitters of one key — separate processes included — agree on a
// single Goal. state.Apply still dedupes keys on replay, for logs written
// before this check existed.
func submitGoal(led *ledger.Ledger, req goalRequest) (api.SubmitResult, error) {
	res := api.SubmitResult{Schema: api.ContractVersion}
	stored, err := led.Update(func(events []api.Event) ([]api.Event, error) {
		s, err := state.Fold(events)
		if err != nil {
			return nil, err
		}
		if req.Key != "" {
			if id, ok := s.KeyToGoal[req.Key]; ok {
				res.GoalID, res.Duplicate = id, true
				if g := s.Goals[id]; g != nil {
					res.Seq = g.SubmittedSeq
				}
				return nil, nil
			}
		}
		// Ids are 32 random bits: a collision is unlikely but not impossible,
		// and replay would silently drop the second Goal of that id.
		goalID := newGoalID()
		for s.Goals[goalID] != nil {
			goalID = newGoalID()
		}
		ev, err := api.NewEvent(api.GoalSubmitted, req.Source, api.GoalSubmittedPayload{
			GoalID: goalID, Text: req.Text, Source: req.Source, IdempotencyKey: req.Key,
			Ref: req.Ref, By: req.By,
		})
		if err != nil {
			return nil, err
		}
		res.GoalID = goalID
		return []api.Event{ev}, nil
	})
	if err != nil {
		return api.SubmitResult{}, err
	}
	if len(stored) > 0 {
		res.Seq = stored[0].Seq
	}
	return res, nil
}

// printJSON writes v to stdout as one compact JSON line: the --json output of
// the write verbs, whose shapes are the contract types in pkg/api.
func printJSON(v any) error {
	if err := json.NewEncoder(os.Stdout).Encode(v); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	return nil
}

// cmdAmend appends steering guidance to a Goal mid-run (GoalAmended). Future
// dispatches and retries carry the amended context; a running worker finishes
// its current attempt uninterrupted. Run `aoa run` afterward to act on it.
func cmdAmend(args []string) error {
	fs := flag.NewFlagSet("amend", flag.ExitOnError)
	describe(fs, "aoa amend \u2014 append steering guidance to a Goal already on the log.\n\nFuture dispatches pick it up; work already in flight is unaffected.", "aoa amend --path ./workspace g-1a2b3c4d \"prefer table-driven tests\"")
	path := fs.String("path", ".", "workspace root")
	asJSON := fs.Bool("json", false, "print the result as one JSON line (pkg/api AmendResult)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) < 2 {
		return fmt.Errorf("usage: aoa amend <goal-id> \"new guidance\"")
	}
	if err := rejectStrayFlags(rest); err != nil {
		return err
	}
	goalID := rest[0]
	guidance := strings.TrimSpace(strings.Join(rest[1:], " "))
	if guidance == "" {
		return fmt.Errorf("amendment guidance is required: aoa amend %s \"...\"", goalID)
	}
	ws, err := openWorkspace(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return err
	}
	res, err := amendGoal(led, goalID, guidance)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(res)
	}
	fmt.Printf("amended goal %s — run `aoa run --path %s` to apply it to pending work\n", goalID, *path)
	return nil
}

// amendGoal appends a GoalAmended event for a Goal that must already be on the
// log. The check and the append are one ledger transaction.
func amendGoal(led *ledger.Ledger, goalID, guidance string) (api.AmendResult, error) {
	stored, err := led.Update(func(events []api.Event) ([]api.Event, error) {
		s, err := state.Fold(events)
		if err != nil {
			return nil, err
		}
		if s.Goals[goalID] == nil {
			return nil, fmt.Errorf("unknown goal %q", goalID)
		}
		ev, err := api.NewEvent(api.GoalAmended, "human", api.GoalAmendedPayload{GoalID: goalID, Guidance: guidance})
		if err != nil {
			return nil, err
		}
		return []api.Event{ev}, nil
	})
	if err != nil {
		return api.AmendResult{}, err
	}
	return api.AmendResult{Schema: api.ContractVersion, GoalID: goalID, Seq: stored[0].Seq}, nil
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	describe(fs, "aoa run \u2014 drive the Scheduler until all work is settled, then exit.\n\nIdempotent and crash-safe: re-running is always allowed and does nothing when\nthere is nothing to do. Exits non-zero if a task failed during this run (older\nfailures are `aoa status`'s business), and 75 if another aoa run is already\nreconciling this workspace or the repository it adopted.", "aoa run --path ./workspace")
	path := fs.String("path", ".", "workspace root")
	once := fs.Bool("once", false, "run a single reconcile pass instead of looping")
	interval := fs.Duration("interval", 0, "keep running, reconciling again every <dur> until interrupted (0 = run until settled, then exit)")
	otelExport := fs.Bool("otel", false, "after the run, replay the Event Log to OTLP (needs OTEL_EXPORTER_OTLP_ENDPOINT)")
	otelLive := fs.Bool("otel-live", false, "stream spans to OTLP live as events happen (instead of one post-hoc export)")
	maxUSD := fs.Float64("max-usd", 0, "budget for this run in USD; past it no new attempt or Goal starts (0 = no limit)")
	maxTokens := fs.Int("max-tokens", 0, "budget for this run in tokens (0 = no limit)")
	maxGoals := fs.Int("max-goals", 0, "how many Goals this run may start (0 = no limit)")
	_ = fs.Parse(args)

	if *once && *interval > 0 {
		return fmt.Errorf("--once and --interval are mutually exclusive")
	}

	ws, err := openWorkspace(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return err
	}
	// Where this run's history starts. A workspace outlives the run that failed
	// in it, so the exit status below counts only what fails past this point
	// (#156); everything older is `aoa status`'s business, not an alert's.
	since, err := lastSeq(led)
	if err != nil {
		return err
	}
	runBudget := state.Budget{USD: *maxUSD, Tokens: *maxTokens, Goals: *maxGoals}
	// A USD or a token budget bounds spend; --max-goals alone does not (#192).
	if cfg, cerr := config.Load(ws.configPath); cerr == nil && cfg.Budget.RequireRunBudget && runBudget.USD == 0 && runBudget.Tokens == 0 {
		return &exitError{code: 2, err: fmt.Errorf("this workspace sets [budget] require_run_budget, so `aoa run` needs --max-usd or --max-tokens (nothing runs here unbudgeted)")}
	}
	ctx := context.Background()

	// start wires the Scheduler. With --otel-live it also opens spans for any
	// in-flight work, then has the ledger feed each new event to the emitter as
	// it's appended (off unless an endpoint is set).
	var o *orchestrator.Orchestrator
	var live *otel.Live
	start := func() error {
		var err error
		if o, err = buildOrchestrator(ws, led, runBudget); err != nil {
			return err
		}
		if !*otelLive {
			return nil
		}
		if !otel.Enabled() {
			fmt.Fprintln(os.Stderr, "note: --otel-live set but OTEL_EXPORTER_OTLP_ENDPOINT is unset; skipping")
			return nil
		}
		if live, err = otel.NewLive(ctx); err != nil {
			return err
		}
		if seed, rerr := led.Read(); rerr == nil {
			live.Seed(seed)
		}
		led.SetAppendHook(live.Observe)
		return nil
	}

	switch {
	case *interval > 0:
		// runEvery takes the Scheduler lock pass by pass, not for its lifetime.
		if err = start(); err == nil {
			sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			defer stop()
			err = runEvery(sigCtx, o, led, ws, *interval)
		}
	default:
		// Lock first, so a run that finds the workspace busy exits without
		// preflighting the backend. --once promises a single pass, so only a run
		// to settled re-checks the log for goals appended while it held the lock.
		err = withSchedulerLock(ws, led, !*once, func() error {
			if o == nil {
				if err := start(); err != nil {
					return err
				}
			}
			if *once {
				return o.ReconcileOnce(ctx)
			}
			return o.Run(ctx)
		})
	}
	if live != nil {
		led.SetAppendHook(nil)
		if serr := live.Shutdown(ctx); serr != nil && err == nil {
			err = serr
		}
	}
	if errors.Is(err, errSchedulerBusy) {
		return &exitError{code: exitSchedulerBusy, err: err}
	}
	if err != nil {
		return err
	}
	if *interval > 0 {
		return nil // runEvery already printed status after each pass
	}

	cfg, err := config.Load(ws.configPath)
	if err != nil {
		return err
	}
	if *otelExport {
		if err := exportOTel(ws, led, cfg); err != nil {
			return err
		}
	}
	_, failed, err := printStatus(led, cfg.Pricing, dayBudget(cfg), since)
	if err != nil {
		return err
	}
	// A settled run is not a successful one. Every ticket can have failed and
	// `aoa run` would still have exited 0, which made the cron / systemd /
	// Actions recipes in docs/scheduling.md unalertable: "the backend isn't
	// even installed" looked exactly like "all done".
	if failed > 0 {
		return fmt.Errorf("%s failed — run `aoa status` for the reason and the worktree to take over", plural(failed, "task"))
	}
	return nil
}

// plural renders "1 task" / "3 tasks".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// runEvery keeps the workspace converging on a fixed cadence: reconcile to
// settled, wait, reconcile again. It is a convenience for a machine you are
// sitting at — it is not a daemon and holds no state, so nothing is lost if it
// dies. For unattended deployment prefer cron, a systemd timer, or a scheduled
// GitHub Actions workflow driving plain `aoa run`, which needs no supervised
// process at all (see docs/scheduling.md).
//
// A failing cycle is reported and the schedule continues: a transient failure
// (a flaky Gate, a rate-limited backend) should not end the loop. Each pass
// holds the Scheduler lock only while it reconciles, so a pass that finds
// another `aoa run` reconciling the workspace is skipped, not fatal. Returns
// nil when interrupted.
func runEvery(ctx context.Context, o *orchestrator.Orchestrator, led *ledger.Ledger, ws workspace, every time.Duration) error {
	cfg, err := config.Load(ws.configPath)
	if err != nil {
		return err
	}
	for {
		err := withSchedulerLock(ws, led, true, func() error { return o.Run(ctx) })
		switch {
		case errors.Is(err, errSchedulerBusy):
			fmt.Fprintln(os.Stderr, "skipping pass: "+err.Error())
		case err != nil && ctx.Err() != nil:
			return nil
		default:
			if err != nil {
				fmt.Fprintf(os.Stderr, "run: %v\n", err)
			}
			if _, _, err := printStatus(led, cfg.Pricing, dayBudget(cfg), 0); err != nil {
				return err
			}
		}
		fmt.Printf("\nnext pass in %s (ctrl-c to stop)\n", every)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(every):
		}
	}
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
	describe(fs, "aoa otel export \u2014 replay the Event Log to OpenTelemetry as OTLP traces\nand metrics.\n\nNeeds OTEL_EXPORTER_OTLP_ENDPOINT; off by default.", "aoa otel export --path ./workspace")
	path := fs.String("path", ".", "workspace root")
	_ = fs.Parse(args)
	if !otel.Enabled() {
		return fmt.Errorf("no OTLP endpoint configured — set OTEL_EXPORTER_OTLP_ENDPOINT (see docs/integrations/honeycomb.md)")
	}
	ws, err := openWorkspace(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(ws.configPath)
	if err != nil {
		return err
	}
	return exportOTel(ws, led, cfg)
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	describe(fs, "aoa status \u2014 show goals, tasks, per-ticket tokens, run cost, and any\n\"needs human\" handoff left behind by a terminal failure.\n\n"+
		"For programs: --json prints one snapshot as a single JSON line (pkg/api\nStatusView): every goal, where it came from, and its outcome.",
		"aoa status --path ./workspace --watch")
	path := fs.String("path", ".", "workspace root")
	watch := fs.Bool("watch", false, "re-render until all work settles (poll the Event Log)")
	interval := fs.Duration("interval", 2*time.Second, "refresh interval for --watch")
	asJSON := fs.Bool("json", false, "print one snapshot as a single JSON line (pkg/api StatusView)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *asJSON && *watch {
		return fmt.Errorf("--json and --watch cannot be combined: --json prints one snapshot; to follow changes, stream the log with `aoa events --json --since <last_seq> --follow`")
	}
	ws, err := openWorkspace(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return err
	}
	cfg, err := config.Load(ws.configPath)
	if err != nil {
		return err
	}

	if *asJSON {
		events, err := led.Read()
		if err != nil {
			return err
		}
		v, _, err := statusView(events, cfg.Pricing, dayBudget(cfg))
		if err != nil {
			return err
		}
		return printJSON(v)
	}
	if !*watch {
		_, _, err = printStatus(led, cfg.Pricing, dayBudget(cfg), 0)
		return err
	}
	// Watch mode: clear + re-render each interval until settled. No daemon — just
	// a poll loop the user can Ctrl-C out of.
	for {
		fmt.Print("\033[H\033[2J") // clear screen, cursor home
		fmt.Printf("aoa status — %s  (Ctrl-C to stop)\n\n", time.Now().Format("15:04:05"))
		settled, _, err := printStatus(led, cfg.Pricing, dayBudget(cfg), 0)
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

// parseWithSubcommand parses flags that may appear on either side of a
// positional subcommand, returning the subcommand (or def if absent).
//
// Go's flag package stops at the first non-flag argument, so a single Parse
// handles at most one of `aoa events tail --count 5` and
// `aoa events --path DIR tail --count 5`. Both are documented, and before this
// both silently dropped --count and printed the default 20. Parsing in a loop —
// consume flags, take the first positional, continue after it — accepts either.
func parseWithSubcommand(fs *flag.FlagSet, args []string, def string) (string, error) {
	sub := ""
	for {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		if fs.NArg() == 0 {
			break
		}
		if sub == "" {
			sub = fs.Arg(0)
		} else if fs.Arg(0) != sub {
			return "", fmt.Errorf("unexpected argument %q", fs.Arg(0))
		}
		args = fs.Args()[1:]
	}
	if sub == "" {
		sub = def
	}
	return sub, nil
}

func cmdEvents(args []string) error {
	fs := flag.NewFlagSet("events", flag.ExitOnError)
	describe(fs, "aoa events \u2014 inspect the Event Log, the single source of truth every\nother number is derived from.\n\n"+
		"Subcommands: tail (most recent events, the default), replay (all of them).\n\n"+
		"For programs: --json prints each event as its Event Log line, byte for byte\n"+
		"(JSONL), and --since N prints every event after seq N, under tail and replay\n"+
		"alike. Resume with --since set to the last seq you received. --type filters\n"+
		"what is printed, not the cursor, so filtered output can skip seqs.\n\n"+
		"--follow keeps printing events as they are appended, until interrupted.",
		"aoa events --path ./workspace --json --since 41 --follow")
	path := fs.String("path", ".", "workspace root")
	count := fs.Int("count", 20, "number of events for tail (0 = all); cannot combine with --since")
	typ := fs.String("type", "", "print only events of this type (filters output, not the --since cursor)")
	asJSON := fs.Bool("json", false, "print each event's Event Log line byte for byte (JSONL)")
	since := fs.Int("since", 0, "print every event after seq `N`, however many (an exclusive cursor)")
	follow := fs.Bool("follow", false, "then keep printing events as they are appended, until interrupted")
	poll := fs.Duration("poll", 500*time.Millisecond, "how often --follow checks the Event Log")
	sub, err := parseWithSubcommand(fs, args, "tail")
	if err != nil {
		return err
	}
	if sub != "tail" && sub != "replay" {
		return fmt.Errorf("unknown events subcommand %q (want tail|replay)", sub)
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["since"] {
		if set["count"] {
			return fmt.Errorf("--since and --count cannot be combined: --since prints every event after seq %d, and --count would silently drop some", *since)
		}
		if *since < 0 {
			return fmt.Errorf("--since takes a seq, 0 or more; got %d", *since)
		}
	}
	if *poll <= 0 {
		return fmt.Errorf("--poll must be positive, got %s", *poll)
	}
	ws, err := openWorkspace(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return err
	}
	all, _, err := led.ReadFrom(0)
	if err != nil {
		return err
	}

	format := formatSummary
	switch {
	case *asJSON:
		format = formatJSON
	case sub == "replay":
		format = formatPayload
	}
	n := *count
	if sub == "replay" {
		n = 0
	}
	lines := filterEvents(all, *typ)
	if set["since"] {
		lines, n = afterSeq(lines, *since), 0
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := bufio.NewWriter(os.Stdout)
	for _, l := range lines {
		if err := writeEvent(out, l, format); err != nil {
			return err
		}
	}
	if err := out.Flush(); err != nil {
		return fmt.Errorf("write events: %w", err)
	}
	if !*follow {
		return nil
	}
	// Follow on from everything just read, printed or not, so nothing is
	// printed twice and nothing appended since is missed.
	cursor := *since
	if n := len(all); n > 0 && all[n-1].Event.Seq > cursor {
		cursor = all[n-1].Event.Seq
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return followEvents(ctx, led, cursor, *typ, format, os.Stdout, *poll)
}

// followEvents prints every event after seq since, then checks the log every
// poll and prints what was appended, in order, until ctx is done; it then
// returns nil. Only complete lines are printed: a line a writer is still in
// the middle of waits for a later poll. typ filters what is printed, not the
// cursor.
//
// If the log is truncated or replaced, it is read again from the start and
// events numbered at or below the last seq already seen are skipped, so a
// restored copy of the same log resumes without repeats. A different log that
// numbers from 1 again prints nothing until it passes that seq.
func followEvents(ctx context.Context, led *ledger.Ledger, since int, typ string, format eventFormat, w io.Writer, poll time.Duration) error {
	out := bufio.NewWriter(w)
	tick := time.NewTicker(poll)
	defer tick.Stop()
	last := since
	var offset int64
	for {
		lines, next, err := led.ReadFrom(offset)
		if offset > 0 && errors.Is(err, ledger.ErrLogShrank) {
			offset = 0
			continue
		}
		if err != nil {
			return err
		}
		offset = next
		for _, l := range lines {
			if l.Event.Seq <= last {
				continue
			}
			last = l.Event.Seq
			if typ != "" && string(l.Event.Type) != typ {
				continue
			}
			if err := writeEvent(out, l, format); err != nil {
				return err
			}
		}
		if err := out.Flush(); err != nil {
			return fmt.Errorf("write events: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// filterEvents keeps only events of the given type ("" = all).
func filterEvents(lines []ledger.RawLine, typ string) []ledger.RawLine {
	if typ == "" {
		return lines
	}
	out := make([]ledger.RawLine, 0, len(lines))
	for _, l := range lines {
		if string(l.Event.Type) == typ {
			out = append(out, l)
		}
	}
	return out
}

// afterSeq keeps only events with a seq greater than since: the events a
// reader who has already seen since has not.
func afterSeq(lines []ledger.RawLine, since int) []ledger.RawLine {
	out := make([]ledger.RawLine, 0, len(lines))
	for _, l := range lines {
		if l.Event.Seq > since {
			out = append(out, l)
		}
	}
	return out
}

// eventFormat is how `aoa events` prints one event.
type eventFormat int

const (
	formatSummary eventFormat = iota // "#seq type id", for tail
	formatPayload                    // the summary plus the raw payload, for replay
	formatJSON                       // the Event Log line itself, byte for byte (--json)
)

// writeEvent prints one Event Log line to w in format f. formatJSON writes the
// stored bytes rather than re-encoding the event, so envelope fields this build
// does not know about reach the consumer intact.
func writeEvent(w io.Writer, l ledger.RawLine, f eventFormat) error {
	var err error
	switch f {
	case formatJSON:
		_, err = fmt.Fprintf(w, "%s\n", l.Bytes)
	case formatPayload:
		_, err = fmt.Fprintf(w, "%s  %s\n", formatEvent(l.Event), l.Event.Payload)
	default:
		_, err = fmt.Fprintln(w, formatEvent(l.Event))
	}
	if err != nil {
		return fmt.Errorf("write event: %w", err)
	}
	return nil
}

func cmdBench(args []string) error {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	describe(fs, "aoa bench \u2014 run the hermetic coordination benchmark and print a report.\n\nOffline and deterministic: uses the mock backend, makes no network calls.", "aoa bench --json")
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
	describe(fs, "aoa eval \u2014 run end-to-end tasks against real repositories.\n\nReports per-task success, tokens, cost and a MAST failure-mode histogram.\nReads aoa.toml from the current directory for spend governors and pricing.", "aoa eval --tasks tasks.toml --backend grok --max-cost 5")
	tasksPath := fs.String("tasks", "", "path to a TOML task file")
	backendName := fs.String("backend", "mock", "agent backend: mock|claudecode|codex|cursor|gemini|grok|openai|anthropic (or a configured plugin)")
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
	cfg, err := config.Load(config.FileName)
	if err != nil {
		return err
	}
	// Only override when --backend was actually passed. This used to fire
	// unconditionally with the flag's "mock" default, so a workspace configured
	// for a real backend silently evaluated the mock and reported a clean run.
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "backend" {
			cfg.Backend = *backendName
		}
	})
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
	// The between-task --max-cost check below cannot stop a single runaway task,
	// so hand the per-goal governors to each task's orchestrator too.
	limits := liveeval.Limits{
		Concurrency:      cfg.Concurrency,
		MaxTokensPerGoal: cfg.MaxTokensPerGoal,
		MaxUsdPerGoal:    cfg.MaxUsdPerGoal,
		Pricing:          cfg.Pricing,
		Conventions:      readConventions(".", cfg.ConventionsFile),
	}
	if priceMap != nil {
		limits.Pricing = priceMap
	}

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
		rep, err := liveeval.Run(ctx, backend, dir, t, limits)
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
	describe(fs, fmt.Sprintf("aoa %s \u2014 decide a proposal parked by the approval gate.\n\nOnly applies when require_approval is set. The proposal has already passed\nthe Gate; this is the human decision on top of it. Repeating a decision\nalready made succeeds and records nothing new.", name),
		fmt.Sprintf("aoa %s --path ./workspace g-1a2b3c4d-impl", name))
	path := fs.String("path", ".", "workspace root")
	asJSON := fs.Bool("json", false, "print the result as one JSON line (pkg/api DecisionResult)")
	by := fs.String("by", "", "who is deciding (recorded as given)")
	reason := fs.String("reason", "", "why (reject defaults to \"rejected by operator\")")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ticketID := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if ticketID == "" {
		return fmt.Errorf("ticket id is required: aoa %s <ticket-id>", name)
	}
	if err := rejectStrayFlags(fs.Args()); err != nil {
		return err
	}
	ws, err := openWorkspace(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return err
	}
	res, err := decideTicket(led, decisionRequest{TicketID: ticketID, Approve: approve, By: *by, Reason: *reason})
	if err != nil {
		return err
	}
	switch {
	case *asJSON:
		return printJSON(res)
	case res.AlreadyDecided:
		fmt.Printf("%s already %s\n", ticketID, res.Decision)
	case approve:
		fmt.Printf("approved %s — run `aoa run --path %s` to merge it\n", ticketID, *path)
	default:
		fmt.Printf("rejected %s\n", ticketID)
	}
	return nil
}

// decisionRequest is a human decision on a parked proposal. By and Reason are
// recorded as given.
type decisionRequest struct {
	TicketID string
	Approve  bool
	By       string
	Reason   string
}

// decideTicket records an approval or rejection of a ticket awaiting approval.
// Repeating the decision already on the log appends nothing and reports
// AlreadyDecided, so a front door can retry safely; contradicting it, or
// deciding a ticket that was never parked, is an error. The check and the
// append are one ledger transaction.
func decideTicket(led *ledger.Ledger, req decisionRequest) (api.DecisionResult, error) {
	decision := api.DecisionApproved
	if !req.Approve {
		decision = api.DecisionRejected
	}
	res := api.DecisionResult{Schema: api.ContractVersion, TicketID: req.TicketID, Decision: decision}
	stored, err := led.Update(func(events []api.Event) ([]api.Event, error) {
		s, err := state.Fold(events)
		if err != nil {
			return nil, err
		}
		t := s.Tickets[req.TicketID]
		switch {
		case t == nil:
			return nil, fmt.Errorf("unknown ticket %q", req.TicketID)
		case t.Status == state.StatusAwaiting:
			// Decide it below.
		case (req.Approve && t.Approved) || (!req.Approve && t.Rejected):
			res.AlreadyDecided, res.Seq = true, t.DecidedSeq
			return nil, nil
		case t.Approved || t.Rejected:
			made := api.DecisionApproved
			if t.Rejected {
				made = api.DecisionRejected
			}
			return nil, fmt.Errorf("ticket %q was already %s; it cannot be %s", req.TicketID, made, decision)
		default:
			return nil, fmt.Errorf("ticket %q is %s, not awaiting approval", req.TicketID, t.Status)
		}
		var ev api.Event
		if req.Approve {
			ev, err = api.NewEvent(api.ApprovalGranted, "human", api.ApprovalGrantedPayload{
				TicketID: req.TicketID, By: req.By, Reason: req.Reason,
			})
		} else {
			reason := req.Reason
			if reason == "" {
				reason = "rejected by operator"
			}
			ev, err = api.NewEvent(api.ApprovalDenied, "human", api.ApprovalDeniedPayload{
				TicketID: req.TicketID, By: req.By, Reason: reason,
			})
		}
		if err != nil {
			return nil, err
		}
		return []api.Event{ev}, nil
	})
	if err != nil {
		return api.DecisionResult{}, err
	}
	if len(stored) > 0 {
		res.Seq = stored[0].Seq
	}
	return res, nil
}

// cmdCancel withdraws a Goal (GoalCancelled) so none of its work lands: the
// Scheduler dispatches nothing more for it and fails its tasks that are not in
// flight, parked proposals included; an attempt already running finishes and
// its proposal is failed. Run `aoa run` afterwards to let the Scheduler act.
func cmdCancel(args []string) error {
	fs := flag.NewFlagSet("cancel", flag.ExitOnError)
	describe(fs, "aoa cancel \u2014 withdraw a Goal so none of its work lands.\n\nThe next `aoa run` fails its queued and parked tasks; an attempt already\nrunning finishes, and its proposal is failed instead of merged. Cancelling a\nGoal already cancelled succeeds and records nothing new.",
		"aoa cancel --path ./workspace --reason \"issue closed\" g-1a2b3c4d")
	path := fs.String("path", ".", "workspace root")
	asJSON := fs.Bool("json", false, "print the result as one JSON line (pkg/api CancelResult)")
	by := fs.String("by", "", "who is cancelling (recorded as given)")
	reason := fs.String("reason", "", "why (recorded as given)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	goalID := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if goalID == "" {
		return fmt.Errorf("goal id is required: aoa cancel <goal-id>")
	}
	if err := rejectStrayFlags(fs.Args()); err != nil {
		return err
	}
	ws, err := openWorkspace(*path)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return err
	}
	res, err := cancelGoal(led, cancelRequest{GoalID: goalID, By: *by, Reason: *reason})
	if err != nil {
		return err
	}
	switch {
	case *asJSON:
		return printJSON(res)
	case res.AlreadyCancelled:
		fmt.Printf("goal %s already cancelled\n", goalID)
	default:
		fmt.Printf("cancelled goal %s\n", goalID)
	}
	return nil
}

// cancelRequest withdraws one Goal. By and Reason are recorded as given.
type cancelRequest struct{ GoalID, By, Reason string }

// cancelGoal appends a GoalCancelled for a Goal that has not settled yet.
// Cancelling a Goal already cancelled appends nothing and reports
// AlreadyCancelled, so a front door can retry safely; an unknown Goal, or one
// already merged, delivered or failed, is an error. The check and the append are one
// ledger transaction.
func cancelGoal(led *ledger.Ledger, req cancelRequest) (api.CancelResult, error) {
	res := api.CancelResult{Schema: api.ContractVersion, GoalID: req.GoalID}
	stored, err := led.Update(func(events []api.Event) ([]api.Event, error) {
		s, err := state.Fold(events)
		if err != nil {
			return nil, err
		}
		g := s.Goals[req.GoalID]
		switch outcome := s.GoalOutcome(req.GoalID); {
		case g == nil:
			return nil, fmt.Errorf("unknown goal %q", req.GoalID)
		case g.Cancelled:
			res.AlreadyCancelled, res.Seq = true, g.CancelledSeq
			return nil, nil
		case outcome == api.OutcomeMerged || outcome == api.OutcomeFailed || outcome == api.OutcomeDelivered:
			return nil, fmt.Errorf("goal %q already settled as %s; nothing to cancel", req.GoalID, outcome)
		}
		ev, err := api.NewEvent(api.GoalCancelled, "human", api.GoalCancelledPayload{
			GoalID: req.GoalID, By: req.By, Reason: req.Reason,
		})
		if err != nil {
			return nil, err
		}
		return []api.Event{ev}, nil
	})
	if err != nil {
		return api.CancelResult{}, err
	}
	if len(stored) > 0 {
		res.Seq = stored[0].Seq
	}
	return res, nil
}

// cmdDiagnose prints the MAST-style failure-mode histogram for a workspace's
// Event Log, turning the design's "aligned with MAST" claim into a measured
// property of the actual run (see internal/diagnose).
func cmdDiagnose(args []string) error {
	fs := flag.NewFlagSet("diagnose", flag.ExitOnError)
	describe(fs, "aoa diagnose \u2014 print a MAST-style failure-mode histogram for a run,\ncomputed purely by replaying the Event Log.", "aoa diagnose --path ./workspace --json")
	path := fs.String("path", ".", "workspace root")
	asJSON := fs.Bool("json", false, "emit JSON instead of a markdown table")
	_ = fs.Parse(args)

	ws, err := openWorkspace(*path)
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

// readConventions loads the coding rules injected into every agent prompt.
// A missing or unreadable file is not an error: conventions are optional, and
// failing a whole run over them would be worse than running without them.
func readConventions(root, file string) string {
	if file == "" {
		return ""
	}
	b, err := os.ReadFile(resolve(root, file))
	if err != nil {
		return ""
	}
	return string(b)
}

// buildOrchestrator wires the Scheduler for ws on led, the workspace's Event
// Log. It preflights the backend, so a missing CLI fails here.
func buildOrchestrator(ws workspace, led *ledger.Ledger, runBudget state.Budget) (*orchestrator.Orchestrator, error) {
	cfg, err := config.Load(ws.configPath)
	if err != nil {
		return nil, err
	}
	// The run's budget window is everything appended from here on.
	runSince := 0
	if events, rerr := led.Read(); rerr == nil && len(events) > 0 {
		runSince = events[len(events)-1].Seq
	}
	repo := worktree.OpenRepo(resolve(ws.root, cfg.Repo))
	delivery, err := preflightDelivery(cfg.Delivery, repo.Dir)
	if err != nil {
		return nil, err
	}
	backend, err := buildBackend(cfg)
	if err != nil {
		return nil, err
	}
	conventions := readConventions(ws.root, cfg.ConventionsFile)
	gate := verify.Verifier{Commands: verify.ToCommands(cfg.Verify), Sandbox: cfg.Sandbox, Image: cfg.SandboxImage}
	var backoff time.Duration
	if cfg.RetryBackoff != "" {
		d, err := time.ParseDuration(cfg.RetryBackoff)
		if err != nil {
			return nil, fmt.Errorf("retry_backoff %q: %w", cfg.RetryBackoff, err)
		}
		backoff = d
	}
	var stall time.Duration
	if cfg.StallTimeout != "" {
		d, err := time.ParseDuration(cfg.StallTimeout)
		if err != nil {
			return nil, fmt.Errorf("stall_timeout %q: %w", cfg.StallTimeout, err)
		}
		stall = d
	}
	var agentTimeout time.Duration
	if cfg.AgentTimeout != "" {
		d, err := time.ParseDuration(cfg.AgentTimeout)
		if err != nil {
			return nil, fmt.Errorf("agent_timeout %q: %w", cfg.AgentTimeout, err)
		}
		agentTimeout = d
	}
	var pollInterval time.Duration
	if cfg.PollInterval != "" {
		d, err := time.ParseDuration(cfg.PollInterval)
		if err != nil {
			return nil, fmt.Errorf("poll_interval %q: %w", cfg.PollInterval, err)
		}
		pollInterval = d
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
		AgentTimeout:      agentTimeout,
		PollInterval:      pollInterval,
		MaxPasses:         cfg.MaxPasses,
		MaxGraphDepth:     cfg.MaxGraphDepth,
		MaxTicketsPerGoal: cfg.MaxTicketsPerGoal,
		MaxFanOut:         cfg.MaxFanOut,
		Delivery:          delivery,
		// Budgets (ADR 017): this run's window starts at the log as it is now.
		RunBudget: runBudget,
		RunSince:  runSince,
		DayBudget: state.Budget{USD: cfg.Budget.USDPerDay, Tokens: cfg.Budget.TokensPerDay, Goals: cfg.Budget.GoalsPerDay},
	}
	mq := mergequeue.New(repo, gate)
	if len(cfg.RegressionVerify) > 0 {
		mq.Shadow = verify.Verifier{Commands: verify.ToCommands(cfg.RegressionVerify), Sandbox: cfg.Sandbox, Image: cfg.SandboxImage}
	}
	return orchestrator.New(led, repo, backend, mq, opt), nil
}

// preflightDelivery maps the [delivery] table onto the Scheduler's options.
// In pr mode it checks, before any work starts, that the repository has the
// remote to push to and that the opener's binary is installed; otherwise the
// first sign of either would be a failed delivery after all the work was done.
func preflightDelivery(d config.DeliveryConfig, repoDir string) (orchestrator.Delivery, error) {
	if d.Mode != "pr" {
		return orchestrator.Delivery{}, nil
	}
	if out, err := exec.Command("git", "-C", repoDir, "remote", "get-url", d.Remote).CombinedOutput(); err != nil {
		return orchestrator.Delivery{}, fmt.Errorf("[delivery] mode = \"pr\" pushes to remote %q, which %s does not have: %s",
			d.Remote, repoDir, strings.TrimSpace(string(out)))
	}
	if len(d.OpenPR) > 0 {
		if _, err := exec.LookPath(d.OpenPR[0]); err != nil {
			return orchestrator.Delivery{}, fmt.Errorf("[delivery] open_pr needs %q on your PATH, but it was not found — install it, or change open_pr in %s", d.OpenPR[0], config.FileName)
		}
	}
	return orchestrator.Delivery{Remote: d.Remote, Base: d.Base, OpenPR: d.OpenPR}, nil
}

// requireCLI fails fast when a CLI-driven backend's binary is missing. Without
// this the run dispatched anyway, failed with an exec error, retried until the
// attempt cap, reported the ticket failed — and (before the exit-code fix) still
// exited 0. The answer belongs at startup, where it is one line.
func requireCLI(bin, backend string) error {
	if _, err := exec.LookPath(bin); err != nil {
		return fmt.Errorf("backend %q needs the %q CLI on your PATH, but it was not found — install it, or set a different `backend` in %s", backend, bin, config.FileName)
	}
	return nil
}

func buildBackendSingle(name string, cfg config.Config) (agent.Backend, error) {
	// A [backends.<name>] block shadows a built-in of the same name. That is
	// deliberate: it lets a user correct a preset's flags for a CLI whose
	// interface has moved, without waiting for a release.
	if bCfg, ok := cfg.Backends[name]; ok {
		switch bCfg.Type {
		case "openai_compatible":
			return agent.NewOpenAICompatible(name, bCfg.Model, bCfg.BaseURL, bCfg.APIKeyEnv), nil
		case "cli":
			if bCfg.Bin == "" {
				return nil, fmt.Errorf("backend %q: type = \"cli\" needs bin = \"<binary>\" in %s", name, config.FileName)
			}
			if err := requireCLI(bCfg.Bin, name); err != nil {
				return nil, err
			}
			return agent.NewCLI(name, bCfg.Bin, bCfg.Args), nil
		default:
			return nil, fmt.Errorf("unknown plugin type %q for backend %q (want openai_compatible|cli)", bCfg.Type, name)
		}
	}

	// Every CLI-driven harness is one row of agent.cliPresets. The $PATH check
	// must come *before* the preflight hook: grok's spawns a detached daemon,
	// and the hermetic suite builds that backend with an empty PATH.
	if b, ok := agent.CLIPreset(name); ok {
		bin, _ := agent.CLIPresetBin(name)
		if err := requireCLI(bin, name); err != nil {
			return nil, err
		}
		if pf := agent.CLIPresetPreflight(name); pf != nil {
			pf()
		}
		return b, nil
	}

	switch name {
	case "mock", "":
		return agent.NewMock(), nil
	case "openai":
		return agent.NewOpenAI(), nil
	case "anthropic":
		return agent.NewAnthropic(), nil
	default:
		return nil, fmt.Errorf("unknown backend %q (want mock|%s|openai|anthropic or a configured plugin)",
			name, strings.Join(agent.CLINames(), "|"))
	}
}

// warnInertGovernors says out loud when a spend ceiling cannot be enforced. The
// governors are driven by the token counts a Backend reports; a backend that
// reports none leaves max_usd_per_goal and max_tokens_per_goal silently doing
// nothing, and a governor you believe in but that does not run is worse than no
// governor at all.
func warnInertGovernors(cfg config.Config, w io.Writer) {
	if cfg.MaxTokensPerGoal == 0 && cfg.MaxUsdPerGoal == 0 {
		return
	}
	name := cfg.Backend
	if name == "" || name == "mock" {
		return
	}
	// A configured plugin shadows any preset. A cli block that overrides a
	// usage-reporting preset while still running its binary emits the same
	// output envelope, so the parser reads the same counts and the governors
	// are live; any other plugin reports nothing aoa can bill against.
	if bCfg, isPlugin := cfg.Backends[name]; isPlugin {
		if bCfg.Type == "cli" && agent.CLIOverrideReportsUsage(name, bCfg.Bin) {
			return
		}
	} else {
		if _, isPreset := agent.CLIPreset(name); isPreset && agent.UsageIsReported(name) {
			return
		}
		if name == "openai" || name == "anthropic" {
			return // native HTTP backends read usage straight off the API response
		}
	}
	fmt.Fprintf(w, "aoa: backend %q does not report token usage, so max_tokens_per_goal/max_usd_per_goal cannot be enforced.\n", name)
	fmt.Fprintf(w, "     Have the agent emit an ```%s fence to opt in, or use a backend that reports its own counts.\n", "aoa:usage")
}

func buildBackend(cfg config.Config) (agent.Backend, error) {
	primary, err := buildBackendSingle(cfg.Backend, cfg)
	if err != nil {
		return nil, err
	}
	warnInertGovernors(cfg, os.Stderr)
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
