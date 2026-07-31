package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
	"github.com/stretchr/testify/require"
)

const testSecret = "s3cret"

func commentBody(text string) []byte {
	return []byte(fmt.Sprintf(`{"action":"created","comment":{"body":%q}}`, text))
}

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// post builds a signed issue_comment delivery.
func post(t *testing.T, body []byte, delivery string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", sign(body, testSecret))
	if delivery != "" {
		req.Header.Set("X-GitHub-Delivery", delivery)
	}
	return req
}

// testRunner returns a runner over a fresh workspace whose reconcile step is a
// no-op, so the tests exercise queueing and the log without driving agents.
func testRunner(t *testing.T) (*runner, *ledger.Ledger) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ws := t.TempDir()
	require.NoError(t, cmdInit([]string{"--path", ws, "--repo", "./demo"}))

	r := newRunner(ws)
	r.run = func() error { return nil }

	w, err := workspaceAt(ws)
	require.NoError(t, err)
	led, err := ledger.Open(w.ledgerPath)
	require.NoError(t, err)
	return r, led
}

// goals reads the Goals currently on the log.
func goals(t *testing.T, led *ledger.Ledger) []api.GoalSubmittedPayload {
	t.Helper()
	events, err := led.Read()
	require.NoError(t, err)
	var out []api.GoalSubmittedPayload
	for _, e := range events {
		if e.Type != api.GoalSubmitted {
			continue
		}
		var p api.GoalSubmittedPayload
		require.NoError(t, e.DecodePayload(&p))
		out = append(out, p)
	}
	return out
}

// drain waits for the runner to go idle.
func drain(t *testing.T, r *runner) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		idle := !r.running && len(r.queue) == 0
		r.mu.Unlock()
		if idle {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("runner did not go idle")
}

func TestServeRejectsBadSignature(t *testing.T) {
	r, led := testRunner(t)
	h := webhookHandler(r, testSecret)

	body := commentBody("@aoa fix the build")
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Hub-Signature-256", sign(body, "wrong-secret"))

	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	drain(t, r)
	if got := goals(t, led); len(got) != 0 {
		t.Errorf("an unsigned delivery created goals: %+v", got)
	}
}

func TestServeAcceptsSignedCommand(t *testing.T) {
	r, led := testRunner(t)
	h := webhookHandler(r, testSecret)

	rec := httptest.NewRecorder()
	h(rec, post(t, commentBody("@aoa add a greeting"), "d-1"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
	drain(t, r)

	got := goals(t, led)
	if len(got) != 1 {
		t.Fatalf("goal count = %d, want 1: %+v", len(got), got)
	}
	if got[0].Text != "add a greeting" {
		t.Errorf("goal text = %q, want %q", got[0].Text, "add a greeting")
	}
	if got[0].Source != "github-webhook" {
		t.Errorf("goal source = %q, want github-webhook", got[0].Source)
	}
	if got[0].IdempotencyKey != "github-delivery:d-1" {
		t.Errorf("idempotency key = %q, want github-delivery:d-1", got[0].IdempotencyKey)
	}
}

func TestServeIgnoresNonCommands(t *testing.T) {
	r, led := testRunner(t)
	h := webhookHandler(r, testSecret)

	for _, body := range []string{"just a normal comment", "@aoa", "  "} {
		rec := httptest.NewRecorder()
		h(rec, post(t, commentBody(body), ""))
		if rec.Code != http.StatusOK {
			t.Errorf("body %q: status = %d, want 200 (ignored)", body, rec.Code)
		}
	}
	drain(t, r)
	if got := goals(t, led); len(got) != 0 {
		t.Errorf("non-commands created goals: %+v", got)
	}
}

func TestServeDeduplicatesRedeliveries(t *testing.T) {
	// GitHub delivery is at-least-once. The same delivery id must not fork a
	// second Goal — and the dedupe must hold on replay, not just in memory.
	r, led := testRunner(t)
	h := webhookHandler(r, testSecret)

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		h(rec, post(t, commentBody("@aoa fix the flaky test"), "delivery-42"))
		require.Equal(t, http.StatusAccepted, rec.Code)
		drain(t, r)
	}

	// All three are on the log (the log is append-only and records what arrived)...
	if got := goals(t, led); len(got) != 3 {
		t.Fatalf("GoalSubmitted event count = %d, want 3", len(got))
	}
	// ...but replay collapses them onto one Goal, which is what the Scheduler acts on.
	events, err := led.Read()
	require.NoError(t, err)
	s, err := state.Fold(events)
	require.NoError(t, err)
	if len(s.Goals) != 1 {
		t.Errorf("replayed goal count = %d, want 1 (redeliveries share an idempotency key)", len(s.Goals))
	}
}

func TestServeRunsAreSingleFlight(t *testing.T) {
	// Two deliveries must never drive two concurrent runs: each run owns a Ledger
	// handle that assigns sequence numbers, so overlapping runs would race the log.
	r, _ := testRunner(t)

	var mu sync.Mutex
	concurrent, maxConcurrent, runs := 0, 0, 0
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	r.run = func() error {
		mu.Lock()
		concurrent++
		runs++
		if concurrent > maxConcurrent {
			maxConcurrent = concurrent
		}
		first := runs == 1
		mu.Unlock()
		if first {
			started <- struct{}{}
			<-release // hold the first run open while more deliveries arrive
		}
		mu.Lock()
		concurrent--
		mu.Unlock()
		return nil
	}

	h := webhookHandler(r, testSecret)
	rec := httptest.NewRecorder()
	h(rec, post(t, commentBody("@aoa first"), "d-1"))
	<-started

	// These land mid-run; they must queue, not start a second run.
	for i, id := range []string{"d-2", "d-3"} {
		rec := httptest.NewRecorder()
		h(rec, post(t, commentBody(fmt.Sprintf("@aoa queued %d", i)), id))
		require.Equal(t, http.StatusAccepted, rec.Code)
	}
	close(release)
	drain(t, r)

	mu.Lock()
	defer mu.Unlock()
	if maxConcurrent != 1 {
		t.Errorf("max concurrent runs = %d, want 1", maxConcurrent)
	}
	if runs < 2 {
		t.Errorf("runs = %d, want at least 2 (work queued mid-run must still be reconciled)", runs)
	}
}

func TestServeRejectsNonPost(t *testing.T) {
	r, _ := testRunner(t)
	h := webhookHandler(r, testSecret)

	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, "/webhook", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}
