package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bharadwaj6/ageOfAgents/internal/config"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// eventsWorkspace makes a workspace whose Event Log holds n events: odd seqs
// are GoalSubmitted, even seqs Heartbeat. It returns the workspace root and a
// handle for appending more.
func eventsWorkspace(t *testing.T, n int) (string, *ledger.Ledger) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, config.FileName), []byte("backend = \"mock\"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	ws, err := workspaceAt(root)
	if err != nil {
		t.Fatalf("workspaceAt: %v", err)
	}
	led, err := ledger.Open(ws.ledgerPath)
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	appendEvents(t, led, n)
	return root, led
}

// appendEvents appends n events to led, continuing eventsWorkspace's pattern.
func appendEvents(t *testing.T, led *ledger.Ledger, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		typ, payload := api.Heartbeat, any(api.HeartbeatPayload{Worker: "w"})
		stored, err := led.Read()
		if err != nil {
			t.Fatalf("read ledger: %v", err)
		}
		if len(stored)%2 == 0 { // the next seq is odd
			typ, payload = api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g" + strconv.Itoa(len(stored)+1), Text: "x"}
		}
		e, err := api.NewEvent(typ, "test", payload)
		if err != nil {
			t.Fatalf("NewEvent: %v", err)
		}
		if _, err := led.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
}

// runEvents runs `aoa events` with args and returns what it printed.
func runEvents(t *testing.T, args ...string) string {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = cmdEvents(args) })
	if err != nil {
		t.Fatalf("aoa events %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// jsonSeqs parses --json output and returns each line's seq, in order.
func jsonSeqs(t *testing.T, out string) []int {
	t.Helper()
	var seqs []int
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		var e api.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("output line is not an event: %q: %v", line, err)
		}
		seqs = append(seqs, e.Seq)
	}
	return seqs
}

// seqRange returns lo..hi inclusive.
func seqRange(lo, hi int) []int {
	var s []int
	for i := lo; i <= hi; i++ {
		s = append(s, i)
	}
	return s
}

// --json is for programs. It must hand over the log's own bytes — not a
// re-encoding — so a field a newer aoa adds to the envelope reaches the
// consumer even through an older binary.
func TestEventsJSONIsLedgerVerbatim(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "replay", args: []string{"--json", "replay"}},
		{name: "flag after the subcommand", args: []string{"replay", "--json"}},
		{name: "tail of everything", args: []string{"tail", "--json", "--count", "0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, led := eventsWorkspace(t, 2)
			// A line from a newer writer: an envelope field api.Event does not
			// have, and < and >, which json.Marshal would escape as \u003c, \u003e.
			future := `{"seq":3,"type":"Heartbeat","ts":"2026-09-18T00:00:00Z","actor":"<ci>","trace_id":"4bf92f35","payload":{"worker":"w"}}` + "\n"
			path := filepath.Join(root, ".aoa", "events.jsonl")
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatalf("open log: %v", err)
			}
			if _, err := f.WriteString(future); err != nil {
				t.Fatalf("write future line: %v", err)
			}
			if err := f.Close(); err != nil {
				t.Fatalf("close log: %v", err)
			}
			if _, err := led.Read(); err != nil {
				t.Fatalf("the future line must still replay: %v", err)
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read log: %v", err)
			}

			got := runEvents(t, append([]string{"--path", root}, tt.args...)...)
			if got != string(want) {
				t.Errorf("--json output is not the log verbatim.\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

// --since N is a cursor: every event after N, however many, so a front door
// can resume from the last seq it received without missing or repeating one.
func TestEventsSinceIsExclusiveAndResumable(t *testing.T) {
	const n = 25 // more than tail's default 20, which --since must not apply
	tests := []struct {
		name string
		args []string
		want []int
	}{
		{name: "since 0 is every event", args: []string{"--since", "0"}, want: seqRange(1, n)},
		{name: "since k is exclusive", args: []string{"--since", "22"}, want: seqRange(23, n)},
		{name: "since the last seq is nothing", args: []string{"--since", "25"}},
		{name: "since past the end is nothing", args: []string{"--since", "99"}},
		{name: "replay selects the same events", args: []string{"replay", "--since", "22"}, want: seqRange(23, n)},
		{name: "type filters the output, not the cursor", args: []string{"--since", "20", "--type", "Heartbeat"}, want: []int{22, 24}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := eventsWorkspace(t, n)
			got := jsonSeqs(t, runEvents(t, append([]string{"--path", root, "--json"}, tt.args...)...))
			if !slices.Equal(got, tt.want) {
				t.Errorf("seqs = %v, want %v", got, tt.want)
			}
		})
	}

	t.Run("resuming from the last seq received yields exactly the new events", func(t *testing.T) {
		root, led := eventsWorkspace(t, n)
		first := jsonSeqs(t, runEvents(t, "--path", root, "--json", "--since", "0"))
		if len(first) != n {
			t.Fatalf("first read got %d events, want %d", len(first), n)
		}
		appendEvents(t, led, 2)
		cursor := strconv.Itoa(first[len(first)-1])
		got := jsonSeqs(t, runEvents(t, "--path", root, "--json", "--since", cursor))
		if want := []int{n + 1, n + 2}; !slices.Equal(got, want) {
			t.Errorf("resumed from %s: seqs = %v, want %v", cursor, got, want)
		}
	})
}

// --since means every event after N. An explicit --count would silently cut
// that short, so the pair is refused rather than half-honoured.
func TestEventsSinceRejectsExplicitCount(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string // "" = must succeed
	}{
		{name: "since with count", args: []string{"--since", "3", "--count", "5"}, wantErr: "--count"},
		{name: "count before the subcommand, since after", args: []string{"--count", "5", "tail", "--since", "3"}, wantErr: "--count"},
		{name: "count 0 is still explicit", args: []string{"--since", "3", "--count", "0"}, wantErr: "--count"},
		{name: "negative since", args: []string{"--since", "-1"}, wantErr: "--since"},
		{name: "since alone", args: []string{"--since", "3"}},
		{name: "count alone", args: []string{"--count", "5"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, _ := eventsWorkspace(t, 4)
			var err error
			captureStdout(t, func() { err = cmdEvents(append([]string{"--path", root}, tt.args...)) })
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("accepted %v, want an error naming %s", tt.args, tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("error %q should name %s", err, tt.wantErr)
			}
		})
	}
}
