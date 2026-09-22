package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/config"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// exitWaitTimeout is `aoa wait`'s status when --timeout expires before every
// named Goal is Complete, so a caller can tell "still going" from "failed" (1)
// and "bad usage" (2).
const exitWaitTimeout = 4

// cmdWait blocks until every named Goal's Complete condition is True (ADR 020),
// then exits with their outcome. It only reads: something else — an `aoa run` —
// has to be driving the workspace for anything to change.
func cmdWait(args []string) error {
	fs := flag.NewFlagSet("wait", flag.ExitOnError)
	describe(fs, "aoa wait — block until each named Goal is complete, then exit with its outcome.\n\n"+
		"Complete means nothing more happens to it without new input (its Complete\n"+
		"condition). Exits 0 if every Goal merged or was delivered, 1 if any failed or\n"+
		"was cancelled, 2 on a usage error or an unknown Goal, and 4 if --timeout\n"+
		"expired first. It only reads the Event Log: an `aoa run` must be driving the\n"+
		"workspace, or a queued Goal waits until --timeout.\n\n"+
		"For programs: --json prints each Goal's final view (pkg/api GoalView), one\nline per Goal, in the order named.",
		"aoa wait --path ./workspace --timeout 1h g-1a2b3c4d")
	path := fs.String("path", ".", "workspace root")
	timeout := fs.Duration("timeout", 0, "give up after this long and exit 4 (0 waits indefinitely)")
	poll := fs.Duration("poll", 500*time.Millisecond, "how often to re-read the Event Log")
	asJSON := fs.Bool("json", false, "print each Goal's final view as one JSON line (pkg/api GoalView)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ids := fs.Args()
	if len(ids) == 0 {
		return &exitError{code: 2, err: fmt.Errorf("at least one goal id is required: aoa wait <goal-id> [goal-id ...]")}
	}
	if err := rejectStrayFlags(ids); err != nil {
		return &exitError{code: 2, err: err}
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

	var deadline time.Time
	if *timeout > 0 {
		deadline = time.Now().Add(*timeout)
	}
	for {
		events, err := led.Read()
		if err != nil {
			return err
		}
		// ponytail: re-folds the whole log each poll, as `status --watch` does;
		// fold incrementally from LastSeq if a large log makes that noticeable.
		v, _, err := statusView(events, cfg.Pricing, state.Budget{})
		if err != nil {
			return err
		}
		views, err := namedGoals(v, ids)
		if err != nil {
			return err
		}
		var pending []string
		for _, gv := range views {
			if !goalDone(gv) {
				pending = append(pending, fmt.Sprintf("%s is not complete (%s)", gv.ID, completeReason(gv)))
			}
		}
		timedOut := !deadline.IsZero() && !time.Now().Before(deadline)
		if len(pending) == 0 || timedOut {
			if err := printWaited(views, *asJSON); err != nil {
				return err
			}
			if len(pending) > 0 {
				return &exitError{code: exitWaitTimeout, err: fmt.Errorf("timed out after %s: %s", *timeout, strings.Join(pending, "; "))}
			}
			return waitOutcome(views)
		}
		wait := *poll
		if !deadline.IsZero() {
			wait = min(wait, time.Until(deadline))
		}
		time.Sleep(wait)
	}
}

// namedGoals picks the GoalViews for ids out of v, in the order named. An id the
// Event Log does not hold is a usage error: waiting on it could never end.
func namedGoals(v api.StatusView, ids []string) ([]api.GoalView, error) {
	byID := make(map[string]api.GoalView, len(v.Goals))
	for _, gv := range v.Goals {
		byID[gv.ID] = gv
	}
	views := make([]api.GoalView, 0, len(ids))
	for _, id := range ids {
		gv, ok := byID[id]
		if !ok {
			return nil, &exitError{code: 2, err: fmt.Errorf("no goal %q on the Event Log", id)}
		}
		views = append(views, gv)
	}
	return views, nil
}

// completeReason is the reason on gv's Complete condition.
func completeReason(gv api.GoalView) string {
	for _, c := range gv.Conditions {
		if c.Type == api.ConditionComplete {
			return c.Reason
		}
	}
	return ""
}

// printWaited prints each Goal waited on: its GoalView as one JSON line with
// asJSON, else its id and outcome.
func printWaited(views []api.GoalView, asJSON bool) error {
	for _, gv := range views {
		if asJSON {
			if err := printJSON(gv); err != nil {
				return err
			}
			continue
		}
		fmt.Printf("%s  %s\n", gv.ID, gv.Outcome)
	}
	return nil
}

// waitOutcome is the error `aoa wait` exits with once every Goal is Complete:
// nil if all of them merged or were delivered, else one naming those that
// failed or were cancelled (exit 1).
func waitOutcome(views []api.GoalView) error {
	var bad []string
	for _, gv := range views {
		if gv.Outcome != api.OutcomeMerged && gv.Outcome != api.OutcomeDelivered {
			bad = append(bad, gv.ID+" "+gv.Outcome)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("not every goal landed: %s", strings.Join(bad, ", "))
	}
	return nil
}
