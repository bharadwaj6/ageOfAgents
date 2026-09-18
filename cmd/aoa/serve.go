package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
)

// maxWebhookBody bounds a delivery body; GitHub's own limit is 25MB but nothing
// we parse needs anywhere near that.
const maxWebhookBody = 1 << 20

// commandPrefix is the ChatOps trigger in an issue comment.
const commandPrefix = "@aoa"

// defaultAllow is the --allow default: the author associations GitHub gives
// people with write access to the repository or membership of its org.
const defaultAllow = "OWNER,MEMBER,COLLABORATOR"

// authorAssociations is every value GitHub sends as a comment's
// author_association, roughly most trusted first.
var authorAssociations = []string{
	"OWNER", "MEMBER", "COLLABORATOR", "CONTRIBUTOR",
	"FIRST_TIMER", "FIRST_TIME_CONTRIBUTOR", "MANNEQUIN", "NONE",
}

// parseAllow parses a comma-separated --allow value into the set of author
// associations that may queue work. Matching is case-insensitive and blank
// entries are skipped, but a value GitHub never sends is an error rather than
// silently matching no one, and so is a list with nothing in it.
func parseAllow(s string) (map[string]bool, error) {
	known := make(map[string]bool, len(authorAssociations))
	for _, a := range authorAssociations {
		known[a] = true
	}
	allow := make(map[string]bool)
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		a := strings.ToUpper(part)
		if !known[a] {
			return nil, fmt.Errorf("--allow: unknown author association %q (valid: %s)", part, strings.Join(authorAssociations, ", "))
		}
		allow[a] = true
	}
	if len(allow) == 0 {
		return nil, fmt.Errorf("--allow: need at least one author association (valid: %s)", strings.Join(authorAssociations, ", "))
	}
	return allow, nil
}

// pendingGoal is webhook-supplied work not yet written to the Event Log. key is
// the delivery's idempotency key, so a redelivery is not submitted twice; ref
// is the URL of the issue the command was posted on; by is the commenter's
// login.
type pendingGoal struct{ text, key, ref, by string }

// runner serializes all work the webhook server drives. Two deliveries must
// never produce two concurrent orchestrator runs: each run owns a Ledger handle
// that assigns sequence numbers, so overlapping runs would race the same log and
// the same worktree base.
//
// The HTTP handler therefore never touches the Ledger itself — it only queues
// goals. The single runner goroutine writes them and reconciles, then loops if
// more arrived meanwhile, so late deliveries are queued rather than dropped and
// the handler stays fast.
type runner struct {
	root string
	run  func() error // the reconcile step; injectable for tests

	mu      sync.Mutex
	queue   []pendingGoal
	running bool
}

func newRunner(root string) *runner {
	r := &runner{root: root}
	r.run = func() error { return cmdRun([]string{"--path", r.root}) }
	return r
}

// enqueue adds a goal and starts the runner if it is idle.
func (r *runner) enqueue(g pendingGoal) {
	r.mu.Lock()
	r.queue = append(r.queue, g)
	start := !r.running
	if start {
		r.running = true
	}
	r.mu.Unlock()
	if start {
		go r.loop()
	}
}

func (r *runner) loop() {
	for {
		r.mu.Lock()
		batch := r.queue
		r.queue = nil
		if len(batch) == 0 {
			r.running = false // nothing left; the next enqueue restarts us
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()

		if err := r.submit(batch); err != nil {
			log.Printf("serve: submit goals: %v", err)
			continue // the run would have nothing new to do
		}
		if err := r.run(); err != nil {
			log.Printf("serve: run: %v", err)
		}
	}
}

// submit writes a batch of queued goals to the Event Log. It opens the Ledger
// only here, on the runner goroutine, so no other writer is live.
func (r *runner) submit(batch []pendingGoal) error {
	ws, err := openWorkspace(r.root)
	if err != nil {
		return err
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		return err
	}
	for _, g := range batch {
		res, err := submitGoal(led, goalRequest{
			Text: g.text, Source: "github-webhook", Ref: g.ref, Key: g.key, By: g.by,
		})
		if err != nil {
			return fmt.Errorf("goal %q: %w", g.text, err)
		}
		if res.Duplicate {
			log.Printf("serve: goal %s already submitted (key %q); redelivery ignored", res.GoalID, g.key)
			continue
		}
		log.Printf("serve: submitted goal %s: %q", res.GoalID, g.text)
	}
	return nil
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	describe(fs, "aoa serve \u2014 run a GitHub webhook server.\n\nAn `@aoa <goal>` issue comment queues a Goal and reconciles the workspace.\nOnly commenters whose author_association is in --allow can queue work; any\nother `@aoa` comment is acknowledged and ignored. --secret proves a delivery\ncame from GitHub, not who wrote the comment. Without --secret the endpoint is\nunauthenticated and the allowlist can be forged: anyone who can reach the port\ncan make this machine run an agent against your repo. See SECURITY.md.",
		"aoa serve --path ./workspace --port 8080 --secret $GITHUB_WEBHOOK_SECRET")
	port := fs.Int("port", 8080, "Port to listen on")
	path := fs.String("path", ".", "Workspace root directory")
	secret := fs.String("secret", "", "GitHub webhook secret")
	allowFlag := fs.String("allow", defaultAllow, "Comma-separated author associations that may queue work ("+strings.Join(authorAssociations, ", ")+")")
	_ = fs.Parse(args)

	allow, err := parseAllow(*allowFlag)
	if err != nil {
		return err
	}
	ws, err := openWorkspace(*path)
	if err != nil {
		return err
	}
	if *secret == "" {
		log.Println("serve: warning: no --secret set; webhook signatures are not verified")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", webhookHandler(newRunner(ws.root), *secret, allow))

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", *port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	fmt.Printf("Age of Agents webhook server listening on :%d\n", *port)
	return srv.ListenAndServe()
}

// webhookHandler accepts GitHub issue-comment deliveries and queues any `@aoa
// <goal>` command for the runner. It responds as soon as the work is queued —
// the orchestrator run happens off the request path and may outlive it.
//
// Only a commenter whose author_association is in allow can queue work; a nil
// or empty allow queues nothing. Anyone else's command is answered 200
// "ignored", not an error status, because GitHub redelivers any non-2xx.
func webhookHandler(r *runner, secret string, allow map[string]bool) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, maxWebhookBody))
		if err != nil {
			http.Error(w, "Error reading body", http.StatusBadRequest)
			return
		}
		if secret != "" && !verifySignature(req.Header.Get("X-Hub-Signature-256"), body, []byte(secret)) {
			http.Error(w, "Invalid signature", http.StatusUnauthorized)
			return
		}
		if req.Header.Get("X-GitHub-Event") != "issue_comment" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ignored")
			return
		}

		var payload struct {
			Action  string `json:"action"`
			Comment struct {
				Body              string `json:"body"`
				AuthorAssociation string `json:"author_association"`
				User              struct {
					Login string `json:"login"`
				} `json:"user"`
			} `json:"comment"`
			Issue struct {
				HTMLURL string `json:"html_url"`
				Number  int    `json:"number"`
			} `json:"issue"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			http.Error(w, "Malformed payload", http.StatusBadRequest)
			return
		}
		cmd, ok := parseCommand(payload.Action, payload.Comment.Body)
		if !ok {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ignored")
			return
		}
		// A valid signature proves GitHub sent this, not that the commenter may
		// spend this machine's compute: on a public repo anyone can comment.
		if !allow[payload.Comment.AuthorAssociation] {
			log.Printf("serve: ignored @aoa from %q (%q not in --allow)", payload.Comment.User.Login, payload.Comment.AuthorAssociation)
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, "ignored")
			return
		}

		// Webhook delivery is at-least-once. Keying the Goal on the delivery id
		// makes a redelivery a no-op: submitGoal finds the key already on the
		// log and appends nothing. The log is the seen-set, so this survives a
		// restart in a way an in-process one would not.
		key := ""
		if id := req.Header.Get("X-GitHub-Delivery"); id != "" {
			key = "github-delivery:" + id
		}
		r.enqueue(pendingGoal{text: cmd, key: key, ref: payload.Issue.HTMLURL, by: payload.Comment.User.Login})

		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintln(w, "queued")
	}
}

// parseCommand extracts the goal text from an issue-comment body, reporting
// whether the comment was an `@aoa` command at all.
func parseCommand(action, body string) (string, bool) {
	if action != "created" {
		return "", false
	}
	trimmed := strings.TrimSpace(body)
	if !strings.HasPrefix(trimmed, commandPrefix) {
		return "", false
	}
	cmd := strings.TrimSpace(strings.TrimPrefix(trimmed, commandPrefix))
	return cmd, cmd != ""
}

func verifySignature(signature string, payload, secret []byte) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	expectedMAC := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(signature[7:]), []byte(expectedMAC))
}
