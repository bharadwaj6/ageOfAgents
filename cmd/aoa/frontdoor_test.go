package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/config"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
	"github.com/stretchr/testify/require"
)

// TestMain lets this test binary stand in for the aoa CLI. Run with
// AOA_TEST_CLI=1 it is `aoa`, so a script under test drives the real CLI
// without a separate build: see the front door tests below.
func TestMain(m *testing.M) {
	if os.Getenv("AOA_TEST_CLI") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// The reference GitHub Issues front door (examples/github-issues) is a shell
// script that knows aoa only through the CLI's JSON contract. These tests run
// it against the real CLI and a file-backed stand-in for gh, so a change to the
// contract that breaks a front door fails here, not in someone's cron job.

// ghStub is gh as far as the front door and aoa's default PR opener use it,
// backed by $GH_STUB/state.json. Any other call fails, so the script cannot
// quietly depend on something this stub does not model.
const ghStub = `#!/usr/bin/env bash
# shellcheck disable=SC2016 # $names in jq filters are jq's
set -euo pipefail
state=$GH_STUB/state.json
all="$*"
fail() { echo "gh stub: unsupported call: gh $all" >&2; exit 2; }
save() { jq "$@" "$state" >"$state.tmp"; mv "$state.tmp" "$state"; }
exists() { [[ $(jq --argjson n "$1" 'any(.issues[]; .number == $n)' "$state") == true ]]; }

group=${1:-}
shift || fail
p1='' p2='' paginate='' repo='' st='' label='' limit='' fields='' filter='' body='' remove='' head='' base='' title=''
while [[ $# -gt 0 ]]; do
  case $1 in
  --paginate) paginate=1; shift ;;
  --repo) repo=$2; shift 2 ;;
  --state) st=$2; shift 2 ;;
  --label) label=$2; shift 2 ;;
  --limit) limit=$2; shift 2 ;;
  --json) fields=$2; shift 2 ;;
  --jq) filter=$2; shift 2 ;;
  --body) body=$2; shift 2 ;;
  --remove-label) remove=$2; shift 2 ;;
  --head) head=$2; shift 2 ;;
  --base) base=$2; shift 2 ;;
  --title) title=$2; shift 2 ;;
  -*) fail ;;
  *) if [[ -z $p1 ]]; then p1=$1; elif [[ -z $p2 ]]; then p2=$1; else fail; fi; shift ;;
  esac
done

me=$(jq -r .me "$state")
want=$(jq -r .repo "$state")
[[ -z $repo || $repo == "$want" ]] || fail
if [[ $group == issue && -n $p2 ]] && ! exists "$p2"; then
  echo "GraphQL: Could not resolve to an issue or pull request with the number of $p2." >&2; exit 1
fi

case "$group $p1" in
"api user")
  [[ $filter == .login ]] || fail
  echo "$me" ;;
"api repos/"*)
  [[ -n $paginate && $p1 =~ ^repos/(.+)/issues/([0-9]+)/(events|comments)$ && ${BASH_REMATCH[1]} == "$want" ]] || fail
  n=${BASH_REMATCH[2]} k=${BASH_REMATCH[3]}
  if ! exists "$n"; then echo "gh: Not Found (HTTP 404)" >&2; exit 1; fi
  jq --argjson n "$n" --arg k "$k" '.issues[] | select(.number == $n) | (.[$k] // [])' "$state" ;;
"issue list")
  [[ $repo == "$want" && $st == open && -n $label && $fields == number,title,body,url,author ]] || fail
  jq --arg l "$label" --argjson max "$limit" \
    '[.issues[] | select(.state == "OPEN" and any((.labels // [])[]; .name == $l)) | {number, title, body, url, author}] | .[:$max]' "$state" ;;
"issue view")
  [[ $repo == "$want" && $fields == state,labels ]] || fail
  jq --argjson n "$p2" '.issues[] | select(.number == $n) | {state, labels: (.labels // [])}' "$state" ;;
"issue comment")
  [[ $repo == "$want" && -n $body ]] || fail
  save --argjson n "$p2" --arg me "$me" --arg b "$body" '(.issues[] | select(.number == $n) | .comments) += [{user: {login: $me}, body: $b}]'
  echo "https://github.com/$want/issues/$p2#issuecomment-1" ;;
"issue edit")
  [[ $repo == "$want" && -n $remove ]] || fail
  save --argjson n "$p2" --arg me "$me" --arg l "$remove" '(.issues[] | select(.number == $n)) |=
    if any((.labels // [])[]; .name == $l)
    then .labels |= map(select(.name != $l)) | .events += [{event: "unlabeled", actor: {login: $me}, label: {name: $l}}]
    else . end'
  echo "https://github.com/$want/issues/$p2" ;;
"pr view")
  [[ $fields == url && $filter == .url ]] || fail
  url=$(jq -r --arg b "$p2" '.prs[] | select(.head == $b) | .url' "$state")
  if [[ -z $url ]]; then echo "no pull requests found for branch \"$p2\"" >&2; exit 1; fi
  echo "$url" ;;
"pr create")
  [[ -n $head && -n $base && -n $title && -n $body ]] || fail
  url="https://github.com/$want/pull/$(( $(jq '.prs | length' "$state") + 1 ))"
  save --arg h "$head" --arg b "$base" --arg t "$title" --arg d "$body" --arg u "$url" '.prs += [{head: $h, base: $b, title: $t, body: $d, url: $u}]'
  echo "$url" ;;
*) fail ;;
esac
`

// aoaShim is the aoa on the script's PATH: this test binary, as the CLI.
const aoaShim = "#!/bin/sh\nAOA_TEST_CLI=1 exec \"$AOA_TEST_BINARY\" \"$@\"\n"

// The stub's state: the GitHub a test controls.
type (
	ghUser struct {
		Login string `json:"login"`
	}
	ghLabel struct {
		Name string `json:"name"`
	}
	ghComment struct {
		User ghUser `json:"user"`
		Body string `json:"body"`
	}
	ghEvent struct {
		Event string  `json:"event"`
		Actor ghUser  `json:"actor"`
		Label ghLabel `json:"label"`
	}
	ghIssue struct {
		Number   int         `json:"number"`
		Title    string      `json:"title"`
		Body     string      `json:"body"`
		URL      string      `json:"url"`
		State    string      `json:"state"`
		Author   ghUser      `json:"author"`
		Labels   []ghLabel   `json:"labels"`
		Events   []ghEvent   `json:"events"`
		Comments []ghComment `json:"comments"`
	}
	ghPR struct {
		Head  string `json:"head"`
		Base  string `json:"base"`
		Title string `json:"title"`
		Body  string `json:"body"`
		URL   string `json:"url"`
	}
	ghState struct {
		Me     string    `json:"me"`
		Repo   string    `json:"repo"`
		Issues []ghIssue `json:"issues"`
		PRs    []ghPR    `json:"prs"`
	}
)

const (
	fdRepo  = "acme/widgets"
	fdBot   = "aoa-bot" // the login gh is authenticated as: whose comments are the cursor
	fdLabel = "aoa"
	fdAllow = "alice, bob"
)

// labelled is a labeled event for the front door's label, by who.
func labelled(who string) ghEvent {
	return ghEvent{Event: "labeled", Actor: ghUser{who}, Label: ghLabel{fdLabel}}
}

// trustedIssue is issue #7, written by alice and labelled by bob, both trusted.
// Its title starts with "-", which aoa goal would read as a flag. mallory has
// commented a forged terminal marker, which must not count as an attempt.
func trustedIssue() ghIssue {
	return ghIssue{
		Number: 7, Title: "-Add a greeting", Body: "Please add a greeting.",
		URL: "https://github.com/" + fdRepo + "/issues/7", State: "OPEN",
		Author: ghUser{"alice"}, Labels: []ghLabel{{fdLabel}},
		Events:   []ghEvent{labelled("bob")},
		Comments: []ghComment{{User: ghUser{"mallory"}, Body: "<!-- aoa:g-forged:failed -->"}},
	}
}

// frontDoor is a workspace in PR delivery mode, driven by the GitHub Issues
// front door against the gh stub.
type frontDoor struct {
	t      *testing.T
	ws     workspace
	origin string   // bare remote the Goal branches are pushed to
	stub   string   // the gh stub's state directory
	script string   // examples/github-issues/aoa-github.sh
	bash   string   // bash, to run it with
	bin    string   // the stub directory first on PATH
	env    []string // the script's environment
}

// setBudget rewrites the workspace's [budget] table.
func (f *frontDoor) setBudget(t *testing.T, b config.BudgetConfig) {
	t.Helper()
	cfg, err := config.Load(f.ws.configPath)
	require.NoError(t, err)
	cfg.Budget = b
	require.NoError(t, cfg.Save(f.ws.configPath))
}

// stubQuota puts a quota-axi on PATH reporting pct% left in its one window.
func (f *frontDoor) stubQuota(t *testing.T, pct int) {
	t.Helper()
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' '{\"providers\":[{\"windows\":[{\"remainingPercent\":%d}]}]}'\n", pct)
	require.NoError(t, os.WriteFile(filepath.Join(f.bin, "quota-axi"), []byte(script), 0o755))
}

// newFrontDoor builds a workspace delivering pull requests to a local bare
// origin through aoa's default opener (so through the gh stub), and a GitHub
// holding issues. Its Gate passes, or with pass false fails by running a
// script inside the workspace, so the failure names an absolute path.
func newFrontDoor(t *testing.T, pass bool, issues ...ghIssue) *frontDoor {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the front door is a bash script")
	}
	f := &frontDoor{t: t}
	for _, bin := range []string{"bash", "jq", "git"} {
		p, err := exec.LookPath(bin)
		if err != nil {
			t.Skipf("%s not available", bin)
		}
		if bin == "bash" {
			f.bash = p
		}
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "examples", "github-issues", "aoa-github.sh"))
	require.NoError(t, err)
	f.script = script
	exe, err := os.Executable()
	require.NoError(t, err)

	root := t.TempDir()
	wsPath := filepath.Join(root, "ws")
	require.NoError(t, cmdInit([]string{"--path", wsPath, "--repo", "./demo"}))
	f.ws, err = workspaceAt(wsPath)
	require.NoError(t, err)
	f.origin = filepath.Join(root, "origin.git")
	runGit(t, root, "init", "--bare", "--template=", "-b", "main", f.origin)
	demo := filepath.Join(wsPath, "demo")
	runGit(t, demo, "remote", "add", "origin", f.origin)
	runGit(t, demo, "push", "origin", "main")
	cfg, err := config.Load(f.ws.configPath)
	require.NoError(t, err)
	cfg.Verify = [][]string{{"true"}}
	if !pass {
		gate := filepath.Join(wsPath, "failing-gate")
		require.NoError(t, os.WriteFile(gate, []byte("#!/bin/sh\nexit 1\n"), 0o755))
		cfg.Verify = [][]string{{gate}}
	}
	cfg.Delivery = config.DeliveryConfig{Mode: "pr"} // the default opener: gh pr view || gh pr create
	require.NoError(t, cfg.Save(f.ws.configPath))

	bin := filepath.Join(root, "bin")
	f.bin = bin
	require.NoError(t, os.Mkdir(bin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(ghStub), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bin, "aoa"), []byte(aoaShim), 0o755))
	f.stub = filepath.Join(root, "gh")
	require.NoError(t, os.Mkdir(f.stub, 0o755))
	f.save(ghState{Me: fdBot, Repo: fdRepo, Issues: issues, PRs: []ghPR{}})

	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "PATH" || strings.HasPrefix(k, "AOA_") || strings.HasPrefix(k, "GH_") || strings.HasPrefix(k, "GITHUB_") {
			continue
		}
		f.env = append(f.env, kv)
	}
	f.env = append(f.env,
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GH_STUB="+f.stub,
		"GH_CONFIG_DIR="+t.TempDir(), // were the real gh ever reached, it would be signed out
		"AOA_TEST_BINARY="+exe,
		"AOA_WS="+wsPath,
		"AOA_GH_REPO="+fdRepo,
		"AOA_GH_ALLOW="+fdAllow,
		"AOA_RUN_MAX_USD=5", // a cycle runs on a budget (ADR 017); the no-budget case unsets it
	)
	return f
}

// try runs the front door's subcommand and returns what it logged.
func (f *frontDoor) try(sub string) (string, error) {
	f.t.Helper()
	cmd := exec.Command(f.bash, f.script, sub)
	cmd.Env = f.env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	f.t.Logf("aoa-github.sh %s:\n%s", sub, out.String())
	return out.String(), err
}

// run is try for a subcommand that must succeed.
func (f *frontDoor) run(sub string) string {
	f.t.Helper()
	out, err := f.try(sub)
	require.NoError(f.t, err, "aoa-github.sh %s:\n%s", sub, out)
	return out
}

// load reads GitHub's state from the stub.
func (f *frontDoor) load() ghState {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.stub, "state.json"))
	require.NoError(f.t, err)
	var s ghState
	require.NoError(f.t, json.Unmarshal(b, &s))
	return s
}

// save replaces GitHub's state in the stub.
func (f *frontDoor) save(s ghState) {
	f.t.Helper()
	b, err := json.Marshal(s)
	require.NoError(f.t, err)
	require.NoError(f.t, os.WriteFile(filepath.Join(f.stub, "state.json"), b, 0o644))
}

// issue returns issue n as GitHub now has it.
func (f *frontDoor) issue(n int) ghIssue {
	f.t.Helper()
	for _, is := range f.load().Issues {
		if is.Number == n {
			return is
		}
	}
	f.t.Fatalf("no issue #%d", n)
	return ghIssue{}
}

// edit changes issue n on GitHub, as a person would.
func (f *frontDoor) edit(n int, change func(*ghIssue)) {
	f.t.Helper()
	s := f.load()
	for i := range s.Issues {
		if s.Issues[i].Number == n {
			change(&s.Issues[i])
		}
	}
	f.save(s)
}

// ourComments are the bodies of the comments the front door wrote on issue n.
func (f *frontDoor) ourComments(n int) []string {
	f.t.Helper()
	var bodies []string
	for _, c := range f.issue(n).Comments {
		if c.User.Login == fdBot {
			bodies = append(bodies, c.Body)
		}
	}
	return bodies
}

// goal is the Goal submitted under key, with its outcome ("" when none was).
func (f *frontDoor) goal(key string) (id, outcome string) {
	f.t.Helper()
	_, s := foldWorkspace(f.t, f.ws)
	id = s.KeyToGoal[key]
	if id == "" {
		return "", ""
	}
	return id, s.GoalOutcome(id)
}

// marker is the hidden marker the front door ends its comment on a Goal's outcome with.
func marker(goal, outcome string) string { return "<!-- aoa:" + goal + ":" + outcome + " -->" }

// A trusted, labelled issue becomes one Goal, delivered as a pull request and
// reported by exactly one comment; the label is removed so that nothing more
// happens until a person asks again.
func TestFrontDoorDeliversATrustedIssue(t *testing.T) {
	f := newFrontDoor(t, true, trustedIssue())
	f.run("cycle")

	// mallory's forged marker is not an attempt of ours: this is attempt 0.
	id, outcome := f.goal("gh:" + fdRepo + "#7/0")
	require.NotEmpty(t, id, "the issue should be submitted as attempt 0")
	require.Equal(t, api.OutcomeDelivered, outcome)
	_, s := foldWorkspace(t, f.ws)
	g := s.Goals[id]
	require.Equal(t, "-Add a greeting\n\nPlease add a greeting.", g.Text, "a title starting with - is text, not a flag")
	require.Equal(t, "github", g.Source)
	require.Equal(t, trustedIssue().URL, g.Ref)
	require.Equal(t, "bob", g.By, "the labeller asked for it")

	runGit(t, f.origin, "rev-parse", "--verify", "refs/heads/aoa/"+id)
	prs := f.load().PRs
	require.Len(t, prs, 1)
	comments := f.ourComments(7)
	require.Len(t, comments, 1)
	require.Contains(t, comments[0], marker(id, "delivered"))
	require.Contains(t, comments[0], prs[0].URL)
	require.Empty(t, f.issue(7).Labels, "the label comes off once the outcome is final")

	f.run("cycle")
	require.Len(t, f.ourComments(7), 1, "a second cycle must not comment again")
	require.Equal(t, 1, countEvents(t, f.ws, api.GoalSubmitted), "a second cycle must not submit again")
	require.Len(t, f.load().PRs, 1)
}

// An issue is handed over only when both its author and whoever last applied
// the label are trusted.
func TestFrontDoorIgnoresAnUntrustedIssue(t *testing.T) {
	tests := []struct {
		name  string
		issue func(*ghIssue)
	}{
		{"untrusted author", func(is *ghIssue) { is.Author = ghUser{"mallory"} }},
		{"untrusted labeller", func(is *ghIssue) {
			is.Events = []ghEvent{labelled("bob"), {Event: "unlabeled", Actor: ghUser{"mallory"}, Label: ghLabel{fdLabel}}, labelled("mallory")}
		}},
		{"no labeller on record", func(is *ghIssue) { is.Events = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := trustedIssue()
			tt.issue(&issue)
			f := newFrontDoor(t, true, issue)
			out := f.run("intake")
			require.Contains(t, out, "#7: skipped")
			require.Equal(t, 0, countEvents(t, f.ws, api.GoalSubmitted))
		})
	}
}

// Closing or unlabelling an issue is its stop switch: the next intake cancels
// its Goal, and report says so once.
func TestFrontDoorCancelsAWithdrawnIssue(t *testing.T) {
	tests := []struct {
		name     string
		withdraw func(*ghIssue)
	}{
		{"unlabelled", func(is *ghIssue) {
			is.Labels = nil
			is.Events = append(is.Events, ghEvent{Event: "unlabeled", Actor: ghUser{"alice"}, Label: ghLabel{fdLabel}})
		}},
		{"closed", func(is *ghIssue) { is.State = "CLOSED" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFrontDoor(t, true, trustedIssue())
			f.run("intake")
			id, outcome := f.goal("gh:" + fdRepo + "#7/0")
			require.Equal(t, api.OutcomeQueued, outcome)

			f.edit(7, tt.withdraw)
			f.run("intake")
			_, outcome = f.goal("gh:" + fdRepo + "#7/0")
			require.Equal(t, api.OutcomeCancelled, outcome)

			f.run("report")
			f.run("report")
			comments := f.ourComments(7)
			require.Len(t, comments, 1)
			require.Contains(t, comments[0], marker(id, "cancelled"))
		})
	}
}

// A cancel is reported when the Goal is Complete, not when it is asked for: a
// Goal cancelled with a proposal still parked is Cancelling until the next
// aoa run fails that proposal, and only then does "nothing more will land" hold.
func TestFrontDoorReportsACancelOnceComplete(t *testing.T) {
	f := newFrontDoor(t, true, trustedIssue())
	cfg, err := config.Load(f.ws.configPath)
	require.NoError(t, err)
	cfg.RequireApproval = true
	require.NoError(t, cfg.Save(f.ws.configPath))

	f.run("cycle")
	id, outcome := f.goal("gh:" + fdRepo + "#7/0")
	require.Equal(t, api.OutcomeAwaitingApproval, outcome)
	require.Len(t, f.ourComments(7), 1, "the approval request")

	f.edit(7, func(is *ghIssue) { is.State = "CLOSED" })
	f.run("intake")
	_, outcome = f.goal("gh:" + fdRepo + "#7/0")
	require.Equal(t, api.OutcomeCancelled, outcome)
	f.run("report")
	require.Len(t, f.ourComments(7), 1, "a Cancelling Goal is not reported yet")

	f.run("cycle") // the run fails the parked proposal
	f.run("report")
	comments := f.ourComments(7)
	require.Len(t, comments, 2)
	require.Contains(t, comments[1], marker(id, "cancelled"))
}

// A Goal that fails the Gate is reported once, without the workspace's paths,
// and its label removed. Adding the label again is a retry: a new Goal under
// the next attempt's key.
func TestFrontDoorReportsAFailureAndRetriesOnRelabel(t *testing.T) {
	f := newFrontDoor(t, false, trustedIssue())
	f.run("cycle")

	first, outcome := f.goal("gh:" + fdRepo + "#7/0")
	require.Equal(t, api.OutcomeFailed, outcome)
	comments := f.ourComments(7)
	require.Len(t, comments, 1)
	require.Contains(t, comments[0], marker(first, "failed"))
	require.Contains(t, comments[0], "verification failed: <path>")
	require.NotContains(t, comments[0], f.ws.root, "the workspace's path leaked into the comment")
	require.Empty(t, f.issue(7).Labels)
	require.Empty(t, f.load().PRs, "a failed Goal is never delivered")

	f.edit(7, func(is *ghIssue) {
		is.Labels = []ghLabel{{fdLabel}}
		is.Events = append(is.Events, labelled("alice"))
	})
	f.run("intake")
	second, outcome := f.goal("gh:" + fdRepo + "#7/1")
	require.NotEmpty(t, second, "a relabel after a reported failure should submit attempt 1")
	require.NotEqual(t, first, second)
	require.Equal(t, api.OutcomeQueued, outcome)

	f.run("intake")
	require.Equal(t, 2, countEvents(t, f.ws, api.GoalSubmitted), "a live Goal is not submitted again")
}

// An issue the front door cannot read, here one deleted after its Goal was
// submitted, is skipped rather than stopping the run: the other issues are
// still worked and reported, and the script exits 1 so a scheduler can alert.
func TestFrontDoorSkipsAnIssueItCannotRead(t *testing.T) {
	other := trustedIssue()
	other.Number, other.URL = 8, "https://github.com/"+fdRepo+"/issues/8"
	f := newFrontDoor(t, true, trustedIssue(), other)
	f.run("intake")
	deleted, _ := f.goal("gh:" + fdRepo + "#7/0")
	require.NotEmpty(t, deleted)

	s := f.load()
	s.Issues = s.Issues[1:] // #7 is deleted
	f.save(s)
	out, err := f.try("cycle")
	var ee *exec.ExitError
	require.ErrorAs(t, err, &ee)
	require.Equal(t, 1, ee.ExitCode())
	require.Contains(t, out, "#7: could not read the issue")
	require.Contains(t, out, "#7: could not read its comments")

	id, outcome := f.goal("gh:" + fdRepo + "#8/0")
	require.Equal(t, api.OutcomeDelivered, outcome)
	comments := f.ourComments(8)
	require.Len(t, comments, 1)
	require.Contains(t, comments[0], marker(id, "delivered"))
}

// without returns the front door's environment with one variable removed.
func (f *frontDoor) without(key string) []string {
	out := make([]string, 0, len(f.env))
	for _, kv := range f.env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// tryEnv runs a subcommand with a replacement environment.
func (f *frontDoor) tryEnv(sub string, env []string) (string, error) {
	f.t.Helper()
	saved := f.env
	f.env = env
	defer func() { f.env = saved }()
	return f.try(sub)
}

// Automation runs on a budget or not at all (ADR 017): a cycle without one
// refuses before it submits anything or spends a token.
func TestFrontDoorRefusesACycleWithoutABudget(t *testing.T) {
	f := newFrontDoor(t, true, trustedIssue())
	out, err := f.tryEnv("cycle", f.without("AOA_RUN_MAX_USD"))
	require.Error(t, err, "a cycle without a budget must refuse")
	require.Contains(t, out, "AOA_RUN_MAX_USD")
	require.Equal(t, 0, countEvents(t, f.ws, api.GoalSubmitted), "nothing was submitted")
	require.Empty(t, f.ourComments(7))
}

// One cycle commits to one cycle's spend: intake submits at most
// AOA_MAX_GOALS_PER_CYCLE new Goals, and the rest wait for a later one.
func TestFrontDoorSubmitsAtMostGoalsPerCycle(t *testing.T) {
	second := trustedIssue()
	second.Number, second.Title = 8, "Add a farewell"
	second.URL = "https://github.com/" + fdRepo + "/issues/8"
	f := newFrontDoor(t, true, trustedIssue(), second)

	f.run("intake")
	require.Equal(t, 1, countEvents(t, f.ws, api.GoalSubmitted), "one Goal per cycle by default")
	f.run("intake")
	require.Equal(t, 2, countEvents(t, f.ws, api.GoalSubmitted), "the next cycle takes the other")
}

// A day budget that is spent stops intake too: a Goal submitted now would only
// sit in the workspace until tomorrow.
func TestFrontDoorStopsIntakeWhenTheDayBudgetIsSpent(t *testing.T) {
	second := trustedIssue()
	second.Number, second.Title = 8, "Add a farewell"
	second.URL = "https://github.com/" + fdRepo + "/issues/8"
	f := newFrontDoor(t, true, trustedIssue(), second)
	f.setBudget(t, config.BudgetConfig{GoalsPerDay: 1})

	f.run("cycle")
	require.Equal(t, 1, countEvents(t, f.ws, api.GoalSubmitted))
	out := f.run("intake")
	require.Contains(t, out, "day's budget is spent")
	require.Equal(t, 1, countEvents(t, f.ws, api.GoalSubmitted), "the day's Goal allowance is gone")
}

// The subscription's own quota is the front door's business (ADR 015): with
// AOA_MIN_QUOTA_PCT set and little left, a cycle does nothing at all.
func TestFrontDoorSkipsACycleWhenQuotaIsLow(t *testing.T) {
	f := newFrontDoor(t, true, trustedIssue())
	f.stubQuota(t, 7)

	out, err := f.tryEnv("cycle", append(f.env, "AOA_MIN_QUOTA_PCT=50"))
	require.NoError(t, err, "skipping is not a failure")
	require.Contains(t, out, "below AOA_MIN_QUOTA_PCT")
	require.Equal(t, 0, countEvents(t, f.ws, api.GoalSubmitted))
}
