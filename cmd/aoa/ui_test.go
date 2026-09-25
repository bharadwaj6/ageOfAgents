package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/config"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
	"github.com/stretchr/testify/require"
)

// uiFixture is a mock workspace served by `aoa ui` over a real listener.
type uiFixture struct {
	ws  workspace
	led *ledger.Ledger
	srv *httptest.Server
}

// newUIFixture scaffolds a mock workspace, submits goals (one per text) and,
// when run is set, reconciles them to settled before serving it. approval
// turns on require_approval, so the goals park instead of merging.
func newUIFixture(t *testing.T, run, approval bool, goals ...string) (*uiFixture, []string) {
	t.Helper()
	tmp, ws := newMockWorkspace(t)
	if approval {
		cfg, err := config.Load(ws.configPath)
		require.NoError(t, err)
		cfg.RequireApproval = true
		require.NoError(t, cfg.Save(ws.configPath))
	}
	led, err := ledger.Open(ws.ledgerPath)
	require.NoError(t, err)
	var ids []string
	for _, g := range goals {
		res, err := submitGoal(led, goalRequest{Text: g, Source: "human"})
		require.NoError(t, err)
		ids = append(ids, res.GoalID)
	}
	if run {
		captureStdout(t, func() { require.NoError(t, cmdRun([]string{"--path", tmp})) })
	}
	return serveUI(t, ws, led, uiServer{by: "alice"}), ids
}

// serveUI serves ws with the options set in opts, on loopback.
func serveUI(t *testing.T, ws workspace, led *ledger.Ledger, opts uiServer) *uiFixture {
	t.Helper()
	cfg, err := config.Load(ws.configPath)
	require.NoError(t, err)
	opts.ws, opts.led, opts.cfg, opts.loopback = ws, led, cfg, true
	if opts.poll == 0 {
		opts.poll = 10 * time.Millisecond
	}
	srv := httptest.NewServer(opts.handler())
	t.Cleanup(srv.Close)
	return &uiFixture{ws: ws, led: led, srv: srv}
}

// do sends one request to the fixture. A POST carries a JSON body and the
// headers a same-origin fetch from the page would; hdr overrides them.
func (f *uiFixture) do(t *testing.T, method, path, body string, hdr map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, f.srv.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", f.srv.URL)
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	for k, v := range hdr {
		if k == "Host" {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

// fold replays the fixture's log.
func (f *uiFixture) fold(t *testing.T) ([]api.Event, *state.State) {
	t.Helper()
	events, err := f.led.Read()
	require.NoError(t, err)
	s, err := state.Fold(events)
	require.NoError(t, err)
	return events, s
}

// The page reads the same projection a front door does: /api/status must be
// `aoa status --json`, byte for byte, or the two views could disagree.
func TestUIStatusIsStatusJSON(t *testing.T) {
	f, _ := newUIFixture(t, true, false, "add a greeting function", "add a farewell function")

	code, body := f.do(t, http.MethodGet, "/api/status", "", nil)
	require.Equal(t, http.StatusOK, code, body)
	want := captureStdout(t, func() { require.NoError(t, cmdStatus([]string{"--path", f.ws.root, "--json"})) })
	require.Equal(t, want, body)

	var v api.StatusView
	require.NoError(t, json.Unmarshal([]byte(body), &v))
	require.Len(t, v.Goals, 2)
}

// A goal's page shows its own execution and nothing else: its goal-scoped
// events and the events of its tasks, in log order, and none of another goal's.
func TestUIGoalEventsBelongToTheGoal(t *testing.T) {
	f, ids := newUIFixture(t, true, false, "add a greeting function", "add a farewell function")
	_, s := f.fold(t)

	for _, id := range ids {
		code, body := f.do(t, http.MethodGet, "/api/goals/"+id+"/events", "", nil)
		require.Equal(t, http.StatusOK, code, body)
		var got []api.Event
		require.NoError(t, json.Unmarshal([]byte(body), &got))

		types := map[api.EventType]bool{}
		last := 0
		for _, e := range got {
			require.Greater(t, e.Seq, last, "events are in log order")
			last = e.Seq
			types[e.Type] = true
			var ref struct {
				GoalID   string `json:"goal_id"`
				TicketID string `json:"ticket_id"`
			}
			require.NoError(t, json.Unmarshal(e.Payload, &ref))
			owner := ref.GoalID
			if tk := s.Tickets[ref.TicketID]; tk != nil {
				owner = tk.GoalID
			}
			require.Equal(t, id, owner, "event %d (%s) belongs to another goal", e.Seq, e.Type)
		}
		for _, want := range []api.EventType{api.GoalSubmitted, api.TicketCreated, api.ProposalSubmitted, api.Merged} {
			require.True(t, types[want], "goal %s is missing its %s event", id, want)
		}
	}

	code, body := f.do(t, http.MethodGet, "/api/goals/g-nope/events", "", nil)
	require.Equal(t, http.StatusNotFound, code, body)
}

// Every write is the CLI verb of the same name: the same checks, the same
// result type, the same event on the log, with the origin recorded as "ui" and
// --by as given.
func TestUIWritesAreTheCLIVerbs(t *testing.T) {
	t.Run("submit", func(t *testing.T) {
		f, _ := newUIFixture(t, false, false)
		code, body := f.do(t, http.MethodPost, "/api/goals", `{"text":"  add a parser  ","ref":"https://example.com/i/1","key":"k1"}`, nil)
		require.Equal(t, http.StatusOK, code, body)
		var res api.SubmitResult
		require.NoError(t, json.Unmarshal([]byte(body), &res))
		require.False(t, res.Duplicate)
		_, s := f.fold(t)
		g := s.Goals[res.GoalID]
		require.NotNil(t, g)
		require.Equal(t, "add a parser", g.Text)
		require.Equal(t, "ui", g.Source)
		require.Equal(t, "alice", g.By)
		require.Equal(t, "https://example.com/i/1", g.Ref)

		code, body = f.do(t, http.MethodPost, "/api/goals", `{"text":"again","key":"k1"}`, nil)
		require.Equal(t, http.StatusOK, code, body)
		var dup api.SubmitResult
		require.NoError(t, json.Unmarshal([]byte(body), &dup))
		require.True(t, dup.Duplicate)
		require.Equal(t, res.GoalID, dup.GoalID)
		events, _ := f.fold(t)
		require.Len(t, events, 1, "a duplicate key appends nothing")
	})

	t.Run("amend and cancel", func(t *testing.T) {
		f, ids := newUIFixture(t, false, false, "add a parser")
		code, body := f.do(t, http.MethodPost, "/api/goals/"+ids[0]+"/amend", `{"guidance":"keep the API"}`, nil)
		require.Equal(t, http.StatusOK, code, body)
		code, body = f.do(t, http.MethodPost, "/api/goals/"+ids[0]+"/cancel", `{"reason":"not needed"}`, nil)
		require.Equal(t, http.StatusOK, code, body)
		var res api.CancelResult
		require.NoError(t, json.Unmarshal([]byte(body), &res))
		require.False(t, res.AlreadyCancelled)

		_, s := f.fold(t)
		g := s.Goals[ids[0]]
		require.Equal(t, []string{"keep the API"}, g.Amendments)
		require.True(t, g.Cancelled)
		events, _ := f.fold(t)
		var p api.GoalCancelledPayload
		require.NoError(t, json.Unmarshal(events[len(events)-1].Payload, &p))
		require.Equal(t, "alice", p.By)
		require.Equal(t, "not needed", p.Reason)
	})

	t.Run("approve and reject", func(t *testing.T) {
		f, ids := newUIFixture(t, true, true, "add a greeting function", "add a farewell function")
		_, s := f.fold(t)
		var parked []string
		for _, id := range ids {
			for _, tk := range s.Tickets {
				if tk.GoalID == id && tk.Status == state.StatusAwaiting {
					parked = append(parked, tk.ID)
				}
			}
		}
		require.Len(t, parked, 2, "require_approval parks each goal's proposal")

		code, body := f.do(t, http.MethodPost, "/api/tickets/"+parked[0]+"/approve", `{}`, nil)
		require.Equal(t, http.StatusOK, code, body)
		code, body = f.do(t, http.MethodPost, "/api/tickets/"+parked[1]+"/reject", `{"reason":"wrong approach"}`, nil)
		require.Equal(t, http.StatusOK, code, body)
		var res api.DecisionResult
		require.NoError(t, json.Unmarshal([]byte(body), &res))
		require.Equal(t, api.DecisionRejected, res.Decision)

		_, s = f.fold(t)
		require.True(t, s.Tickets[parked[0]].Approved)
		require.True(t, s.Tickets[parked[1]].Rejected)

		code, body = f.do(t, http.MethodPost, "/api/tickets/"+parked[0]+"/reject", `{}`, nil)
		require.Equal(t, http.StatusConflict, code, "contradicting a decision is refused: %s", body)
	})

	t.Run("refusals", func(t *testing.T) {
		f, ids := newUIFixture(t, true, false, "add a greeting function")
		events, _ := f.fold(t)
		for _, tc := range []struct {
			name, path, body string
			want             int
		}{
			{"empty goal text", "/api/goals", `{"text":"   "}`, http.StatusBadRequest},
			{"unknown field", "/api/goals", `{"text":"x","sneaky":true}`, http.StatusBadRequest},
			{"malformed body", "/api/goals", `{`, http.StatusBadRequest},
			{"empty guidance", "/api/goals/" + ids[0] + "/amend", `{"guidance":""}`, http.StatusBadRequest},
			{"amend an unknown goal", "/api/goals/g-nope/amend", `{"guidance":"x"}`, http.StatusConflict},
			{"cancel a merged goal", "/api/goals/" + ids[0] + "/cancel", `{}`, http.StatusConflict},
			{"approve an unknown task", "/api/tickets/nope/approve", `{}`, http.StatusConflict},
		} {
			t.Run(tc.name, func(t *testing.T) {
				code, body := f.do(t, http.MethodPost, tc.path, tc.body, nil)
				require.Equal(t, tc.want, code, body)
				require.Contains(t, body, `"error"`)
			})
		}
		after, _ := f.fold(t)
		require.Len(t, after, len(events), "a refused write appends nothing")
	})
}

// The page can do what the CLI can, so only this machine's own pages may drive
// it: a request addressed to another hostname (DNS rebinding), a write from
// another origin, a form post, or any write to a read-only view is refused.
func TestUIRefusesRequestsFromElsewhere(t *testing.T) {
	_, ws := newMockWorkspace(t)
	led, err := ledger.Open(ws.ledgerPath)
	require.NoError(t, err)
	rw := serveUI(t, ws, led, uiServer{})
	ro := serveUI(t, ws, led, uiServer{readOnly: true})
	const submit = `{"text":"add a parser"}`

	for _, tc := range []struct {
		name   string
		f      *uiFixture
		method string
		path   string
		hdr    map[string]string
		want   int
	}{
		{"same-origin write", rw, http.MethodPost, "/api/goals", nil, http.StatusOK},
		{"non-browser write", rw, http.MethodPost, "/api/goals", map[string]string{"Origin": "", "Sec-Fetch-Site": ""}, http.StatusOK},
		{"rebound hostname read", rw, http.MethodGet, "/api/status", map[string]string{"Host": "evil.example:7070"}, http.StatusForbidden},
		{"rebound hostname write", rw, http.MethodPost, "/api/goals", map[string]string{"Host": "evil.example:7070"}, http.StatusForbidden},
		{"cross-origin write", rw, http.MethodPost, "/api/goals", map[string]string{"Origin": "http://evil.example"}, http.StatusForbidden},
		{"cross-site fetch", rw, http.MethodPost, "/api/goals", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"form post", rw, http.MethodPost, "/api/goals", map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
		{"read-only write", ro, http.MethodPost, "/api/goals", nil, http.StatusForbidden},
		{"read-only read", ro, http.MethodGet, "/api/status", nil, http.StatusOK},
		{"localhost by name", rw, http.MethodGet, "/api/status", map[string]string{"Host": "localhost:7070"}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := ""
			if tc.method == http.MethodPost {
				body = submit
			}
			code, resp := tc.f.do(t, tc.method, tc.path, body, tc.hdr)
			require.Equal(t, tc.want, code, resp)
		})
	}
}

func TestUIListensBeyondLoopbackOnlyReadOnly(t *testing.T) {
	for _, tc := range []struct {
		addr         string
		readOnly     bool
		wantLoopback bool
		wantErr      bool
	}{
		{addr: "127.0.0.1:7070", wantLoopback: true},
		{addr: "localhost:0", wantLoopback: true},
		{addr: "[::1]:7070", wantLoopback: true},
		{addr: "0.0.0.0:7070", wantErr: true},
		{addr: ":7070", wantErr: true},
		{addr: "192.168.1.5:7070", wantErr: true},
		{addr: "0.0.0.0:7070", readOnly: true},
		{addr: "no-port", wantErr: true},
	} {
		t.Run(tc.addr+"/ro="+strconv.FormatBool(tc.readOnly), func(t *testing.T) {
			loopback, err := checkUIAddr(tc.addr, tc.readOnly)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantLoopback, loopback)
		})
	}
}

// sseEvent is one message read off the event stream.
type sseEvent struct {
	id   int
	data string
}

// readSSE connects to the stream with the given headers and returns a channel
// of its messages; the connection closes when the test ends.
func readSSE(t *testing.T, f *uiFixture, path string, hdr map[string]string) <-chan sseEvent {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.srv.URL+path, nil)
	require.NoError(t, err)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	out := make(chan sseEvent, 64)
	go func() {
		defer resp.Body.Close()
		defer close(out)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		var ev sseEvent
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				n, err := strconv.Atoi(strings.TrimPrefix(line, "id: "))
				if err != nil {
					return
				}
				ev.id = n
			case strings.HasPrefix(line, "data: "):
				ev.data = strings.TrimPrefix(line, "data: ")
			case line == "" && ev.data != "":
				out <- ev
				ev = sseEvent{}
			}
		}
	}()
	return out
}

// nextSSE waits for the next message on the stream.
func nextSSE(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		require.True(t, ok, "stream closed")
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("no event on the stream within 5s")
		return sseEvent{}
	}
}

// The live stream is `aoa events --json --since N --follow`: every Event Log
// line after the cursor, byte for byte, then each one appended, with its seq as
// the SSE id. A reconnecting browser's Last-Event-ID wins over ?since, so a
// dropped connection resumes where it left off rather than where it began.
func TestUIStreamIsTheLogFromACursor(t *testing.T) {
	root, led := eventsWorkspace(t, 5)
	ws, err := openWorkspace(root)
	require.NoError(t, err)
	f := serveUI(t, ws, led, uiServer{})
	lines, _, err := led.ReadFrom(0)
	require.NoError(t, err)

	t.Run("since", func(t *testing.T) {
		ch := readSSE(t, f, "/api/events?since=2", nil)
		for seq := 3; seq <= 5; seq++ {
			ev := nextSSE(t, ch)
			require.Equal(t, seq, ev.id)
			require.Equal(t, string(lines[seq-1].Bytes), ev.data, "the log line itself, not a re-encoding")
		}
		appendEvents(t, led, 1)
		require.Equal(t, 6, nextSSE(t, ch).id, "an append after connecting arrives")
	})

	t.Run("Last-Event-ID wins", func(t *testing.T) {
		ch := readSSE(t, f, "/api/events?since=0", map[string]string{"Last-Event-ID": "5"})
		require.Equal(t, 6, nextSSE(t, ch).id)
	})

	t.Run("bad cursor", func(t *testing.T) {
		code, body := f.do(t, http.MethodGet, "/api/events?since=-1", "", nil)
		require.Equal(t, http.StatusBadRequest, code, body)
	})
}

// A stream opened on a quiet log still reaches the browser at once. The page
// shows "live" when EventSource opens, and that waits for the response
// headers; held in the server's buffer, they would arrive with the first
// keep-alive, uiKeepAlive later.
func TestUIStreamOpensOnAQuietLog(t *testing.T) {
	root, led := eventsWorkspace(t, 5)
	ws, err := openWorkspace(root)
	require.NoError(t, err)
	f := serveUI(t, ws, led, uiServer{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.srv.URL+"/api/events?since=5", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "headers must not wait for an event or a keep-alive")
	defer func() { require.NoError(t, resp.Body.Close()) }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "retry: 2000\n", line)
}

// The page ships inside the binary and works offline (golden rule 6): nothing
// is fetched from another host. And log text reaches the page only as text —
// goal text and Gate output are untrusted agent input (ADR 015 §8).
func TestUIPageIsSelfContained(t *testing.T) {
	_, ws := newMockWorkspace(t)
	led, err := ledger.Open(ws.ledgerPath)
	require.NoError(t, err)
	f := serveUI(t, ws, led, uiServer{})

	for _, tc := range []struct{ path, contentType, contains string }{
		{"/", "text/html", `src="static/app.js"`},
		{"/static/app.js", "javascript", "EventSource"},
		{"/static/app.css", "text/css", "prefers-color-scheme"},
	} {
		code, body := f.do(t, http.MethodGet, tc.path, "", nil)
		require.Equal(t, http.StatusOK, code, tc.path)
		require.Contains(t, body, tc.contains, tc.path)
	}

	for _, name := range []string{"ui/index.html", "ui/app.css", "ui/app.js"} {
		b, err := uiAssets.ReadFile(name)
		require.NoError(t, err)
		src := string(b)
		require.NotContains(t, src, "https://", "%s loads nothing from another host", name)
		require.NotContains(t, src, "http://", "%s loads nothing from another host", name)
		require.NotContains(t, src, "innerHTML", "%s renders log text as text", name)
		require.NotContains(t, src, "insertAdjacentHTML", "%s renders log text as text", name)
	}

	req, err := http.NewRequest(http.MethodGet, f.srv.URL+"/", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Contains(t, resp.Header.Get("Content-Security-Policy"), "default-src 'self'")
	require.Contains(t, resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'")
}

// cmdUI refuses to start in a way that would expose writes, before it listens.
func TestUICommandRefusesANonLoopbackWritableAddr(t *testing.T) {
	tmp, _ := newMockWorkspace(t)
	for _, tc := range []struct {
		name, want string
		args       []string
	}{
		{"non-loopback writable", "--read-only", []string{"--path", tmp, "--addr", "0.0.0.0:0"}},
		{"stray argument", "unexpected argument", []string{"--path", tmp, "--addr", "127.0.0.1:0", "extra"}},
		{"not a workspace", "not an aoa workspace", []string{"--path", t.TempDir(), "--addr", "127.0.0.1:0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A refusal returns at once; a server that started instead would
			// block forever, so that is a failure too, not a hung test.
			done := make(chan error, 1)
			go func() { done <- cmdUI(tc.args) }()
			select {
			case err := <-done:
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.want)
			case <-time.After(5 * time.Second):
				t.Fatal("cmdUI started serving instead of refusing")
			}
		})
	}
}
