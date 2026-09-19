package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/internal/worktree"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Delivery configures pull-request delivery (ADR 016). The zero value is local
// mode: work merges onto the adopted repository's checked-out branch and
// nothing is pushed.
type Delivery struct {
	Remote string   // remote the base is fetched from and Goal branches pushed to; "" = local mode
	Base   string   // branch on Remote each Goal branch is cut from and its pull request targets
	OpenPR []string // opener argv, placeholders {branch} {base} {title} {body}; empty = push only
}

// openerTimeout bounds one run of the opener command.
const openerTimeout = 2 * time.Minute

// reasonMax bounds the command output a DeliveryFailed reason carries.
const reasonMax = 1000

// prMode reports whether verified work is delivered as a pull request per Goal.
func (o *Orchestrator) prMode() bool { return o.opt.Delivery.Remote != "" }

// goalBranch is the branch a Goal's work merges onto in PR mode. It is a
// function of the Goal alone, so a re-run after a crash finds the same branch.
func goalBranch(goalID string) string { return "aoa/" + worktree.SanitizeBranch(goalID) }

// ensureGoalBranch cuts the Goal's branch from the freshly fetched remote base,
// unless it already exists. It fetches once per Goal per process.
func (o *Orchestrator) ensureGoalBranch(ctx context.Context, goalID string) error {
	if o.branched[goalID] {
		return nil
	}
	d := o.opt.Delivery
	if err := o.repo.Fetch(ctx, d.Remote, d.Base); err != nil {
		return fmt.Errorf("fetch %s/%s: %w", d.Remote, d.Base, err)
	}
	if err := o.repo.EnsureBranch(ctx, goalBranch(goalID), "refs/remotes/"+d.Remote+"/"+d.Base); err != nil {
		return fmt.Errorf("cut %s: %w", goalBranch(goalID), err)
	}
	o.branched[goalID] = true
	return nil
}

// deliverReady delivers every Goal whose work is complete on its branch, each
// at most once per Run: a failed delivery is recorded and retried by the next
// `aoa run`, not on every pass of this one.
func (o *Orchestrator) deliverReady(ctx context.Context) error {
	s, err := o.loadState()
	if err != nil {
		return err
	}
	for _, g := range sortedGoals(s) {
		if !s.Deliverable(g.ID) || o.deliveryTried[g.ID] {
			continue
		}
		o.deliveryTried[g.ID] = true
		if err := o.deliver(ctx, g.ID); err != nil {
			return err
		}
	}
	return nil
}

// deliver pushes a complete Goal's branch and opens its pull request, then
// records Delivered — or DeliveryFailed, with why. Only an Event Log failure is
// returned as an error.
func (o *Orchestrator) deliver(ctx context.Context, goalID string) error {
	// A cancel may have landed while this pass merged; never deliver past it.
	s, err := o.loadState()
	if err != nil {
		return err
	}
	g := s.Goals[goalID]
	if !s.Deliverable(goalID) {
		return nil
	}
	branch := goalBranch(goalID)
	failed := func(reason string) error {
		return o.emit(api.DeliveryFailed, api.DeliveryFailedPayload{GoalID: goalID, Branch: branch, Reason: reason})
	}
	tip, err := o.repo.RevParse(ctx, "refs/heads/"+branch)
	if err != nil {
		return failed("read " + branch + ": " + tail(err.Error()))
	}
	if err := o.repo.Push(ctx, o.opt.Delivery.Remote, branch); err != nil {
		return failed("push " + branch + ": " + tail(err.Error()))
	}
	var url string
	if len(o.opt.Delivery.OpenPR) > 0 {
		if url, err = o.openPR(ctx, g, branch); err != nil {
			return failed("open pull request: " + err.Error())
		}
	}
	return o.emit(api.Delivered, api.DeliveredPayload{GoalID: goalID, Branch: branch, Commit: tip, URL: url})
}

// openPR runs the opener in the repository and returns the pull request URL
// it printed: the last stdout line starting with "http". Placeholders are
// replaced inside each argv element, so goal text never reaches a shell parse.
func (o *Orchestrator) openPR(ctx context.Context, g *state.Goal, branch string) (string, error) {
	r := strings.NewReplacer("{branch}", branch, "{base}", o.opt.Delivery.Base, "{title}", prTitle(g.Text), "{body}", prBody(g))
	argv := make([]string, len(o.opt.Delivery.OpenPR))
	for i, a := range o.opt.Delivery.OpenPR {
		argv[i] = r.Replace(a)
	}
	ctx, cancel := context.WithTimeout(ctx, openerTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = o.repo.Dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("timed out after %s: %w", openerTimeout, err)
		}
		return "", fmt.Errorf("%s: %w: %s", argv[0], err, tail(stderr.String()))
	}
	var url string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "http") {
			url = line
		}
	}
	return url, nil
}

// prTitle is the first line of the Goal's text, cut to the subject limit.
func prTitle(text string) string {
	title := strings.TrimSpace(strings.SplitN(strings.TrimSpace(text), "\n", 2)[0])
	if r := []rune(title); len(r) > subjectMax {
		title = strings.TrimSpace(string(r[:subjectMax-1])) + "…"
	}
	return title
}

// prBody is the Goal's text, a closing reference to its origin when that is a
// URL, and a note that every commit on the branch passed the Gate.
func prBody(g *state.Goal) string {
	body := strings.TrimSpace(g.Text)
	if strings.HasPrefix(g.Ref, "https://") || strings.HasPrefix(g.Ref, "http://") {
		body += "\n\nCloses " + g.Ref
	}
	return body + "\n\nDelivered by aoa: every commit on this branch passed the Gate before it landed."
}

// tail keeps the last reasonMax bytes of a command's output, where git and
// most CLIs put the line that says what went wrong.
func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= reasonMax {
		return s
	}
	return "…" + strings.ToValidUTF8(s[len(s)-reasonMax:], "")
}
