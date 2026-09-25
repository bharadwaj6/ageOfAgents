package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bharadwaj6/ageOfAgents/internal/config"
	"github.com/bharadwaj6/ageOfAgents/internal/ledger"
	"github.com/bharadwaj6/ageOfAgents/internal/state"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// uiAssets is the web view's page, script and stylesheet: static files with no
// build step and nothing fetched from elsewhere, so the binary stays offline.
//
//go:embed ui
var uiAssets embed.FS

// maxUIBody bounds a write request's body. Goal text is the largest thing
// posted, and nothing sensible comes near this.
const maxUIBody = 1 << 20

// uiKeepAlive is how long the event stream may stay silent before it sends a
// comment line, so a proxy or browser does not drop an idle connection.
const uiKeepAlive = 15 * time.Second

// uiServer is `aoa ui`: the CLI's read and write verbs over HTTP, for one
// person at a browser on this machine (ADR 021). Reads are the same
// projections `aoa status --json` and `aoa events --json` print; writes call
// the same functions the CLI verbs do, so the page has exactly the CLI's
// authority. It never runs the Scheduler.
type uiServer struct {
	ws       workspace
	led      *ledger.Ledger
	cfg      config.Config
	by       string        // recorded on every write, as given; empty when unset
	readOnly bool          // refuse every write
	loopback bool          // bound to loopback: requests must name a loopback host
	poll     time.Duration // how often the event stream checks the log
}

func cmdUI(args []string) error {
	fs := flag.NewFlagSet("ui", flag.ExitOnError)
	describe(fs, "aoa ui — serve a web view of this workspace: every goal, its conditions,\n"+
		"its tasks and their Gate output, and its event timeline, updated live.\n\n"+
		"The page can submit, amend and cancel goals and approve or reject parked\n"+
		"proposals — the same verbs as the CLI, with the same authority. It never\n"+
		"runs the Scheduler: keep an `aoa run --interval` going beside it.\n\n"+
		"It listens on loopback. A non-loopback --addr is refused unless --read-only\n"+
		"is set; to reach it from elsewhere, tunnel the port over SSH.",
		"aoa ui --path ./workspace --by alice")
	path := fs.String("path", ".", "workspace root")
	addr := fs.String("addr", "127.0.0.1:7070", "address to listen on (host:port)")
	by := fs.String("by", "", "who is acting, recorded on every write (as given)")
	readOnly := fs.Bool("read-only", false, "serve the view only; refuse every write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := rejectStrayFlags(fs.Args()); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	loopback, err := checkUIAddr(*addr, *readOnly)
	if err != nil {
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
	cfg, err := config.Load(ws.configPath)
	if err != nil {
		return err
	}
	s := &uiServer{ws: ws, led: led, cfg: cfg, by: *by, readOnly: *readOnly, loopback: loopback, poll: 500 * time.Millisecond}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Handler:           s.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second, // the event stream lifts it for itself
		IdleTimeout:       60 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	shut := make(chan error, 1)
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		shut <- srv.Shutdown(sctx)
	}()

	mode := ""
	if *readOnly {
		mode = " (read-only)"
	}
	fmt.Printf("aoa ui: http://%s/%s — Ctrl-C to stop\n", ln.Addr(), mode)
	if !loopback {
		fmt.Fprintln(os.Stderr, "aoa ui: warning: listening beyond loopback; anyone who can reach the port can read every goal and its Gate output")
	}
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	stop()
	return <-shut
}

// checkUIAddr validates --addr and reports whether it is loopback. Anything
// else is refused unless the view is read-only: a write from the page has the
// CLI's authority, and that must not reach the network by default.
func checkUIAddr(addr string, readOnly bool) (loopback bool, err error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, fmt.Errorf("--addr %q: %w", addr, err)
	}
	if isLoopbackHost(host) {
		return true, nil
	}
	if !readOnly {
		return false, fmt.Errorf("--addr %q is not loopback: the page can submit and approve work, so it only listens beyond this machine with --read-only (or tunnel the port over SSH)", addr)
	}
	return false, nil
}

// isLoopbackHost reports whether host, a name or an IP without a port, can only
// mean this machine. An empty host (every interface) is not loopback.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// handler routes the view's page, its read API, its event stream and its write
// verbs, behind the request guards.
func (s *uiServer) handler() http.Handler {
	static, err := fs.Sub(uiAssets, "ui")
	if err != nil {
		panic(err) // the embed directive guarantees the directory
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, static, "index.html")
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/goals/{id}/events", s.goalEventsHandler)
	mux.HandleFunc("GET /api/events", s.stream)
	mux.HandleFunc("POST /api/goals", s.write(s.submit))
	mux.HandleFunc("POST /api/goals/{id}/amend", s.write(s.amend))
	mux.HandleFunc("POST /api/goals/{id}/cancel", s.write(s.cancel))
	mux.HandleFunc("POST /api/tickets/{id}/approve", s.write(s.decide(true)))
	mux.HandleFunc("POST /api/tickets/{id}/reject", s.write(s.decide(false)))
	return s.guard(mux)
}

// guard applies to every request. A view bound to loopback answers only
// requests addressed to a loopback name, which defeats DNS rebinding: a page on
// another site that points its own hostname at 127.0.0.1 still sends that
// hostname. Every response forbids framing and anything off-origin.
func (s *uiServer) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		if s.loopback && !isLoopbackHost(hostOnly(r.Host)) {
			writeUIError(w, http.StatusForbidden, fmt.Sprintf("host %q is not loopback", r.Host))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostOnly strips an optional port from a Host header.
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// write wraps a write verb. It refuses in read-only mode, requires a JSON body
// — a cross-site form cannot send one without a CORS preflight, which this
// server never answers — and refuses a browser request from another origin.
// A request with neither Origin nor Sec-Fetch-Site is not from a browser page,
// and has no more authority than the CLI on the same machine.
func (s *uiServer) write(verb func(*http.Request) (any, int, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.readOnly {
			writeUIError(w, http.StatusForbidden, "this view is read-only (aoa ui --read-only)")
			return
		}
		if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
			writeUIError(w, http.StatusUnsupportedMediaType, "a write takes a JSON body (Content-Type: application/json)")
			return
		}
		if !sameOrigin(r) {
			writeUIError(w, http.StatusForbidden, "cross-origin write refused")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxUIBody)
		res, code, err := verb(r)
		if err != nil {
			writeUIError(w, code, err.Error())
			return
		}
		writeUIJSON(w, http.StatusOK, res)
	}
}

// sameOrigin reports whether a request came from this server's own page, as
// far as the browser says: Origin, when sent, must name this host, and
// Sec-Fetch-Site, when sent, must not say another site made it.
func sameOrigin(r *http.Request) bool {
	if o := r.Header.Get("Origin"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.Host != r.Host {
			return false
		}
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	default:
		return false
	}
}

// uiInfo tells the page what it may offer: whether writes are refused, and the
// name they are recorded under.
type uiInfo struct {
	Workspace string `json:"workspace"`
	ReadOnly  bool   `json:"read_only"`
	By        string `json:"by,omitempty"`
}

func (s *uiServer) info(w http.ResponseWriter, _ *http.Request) {
	writeUIJSON(w, http.StatusOK, uiInfo{Workspace: s.ws.root, ReadOnly: s.readOnly, By: s.by})
}

// status is `aoa status --json`: the same projection, the same bytes.
func (s *uiServer) status(w http.ResponseWriter, _ *http.Request) {
	events, err := s.led.Read()
	if err != nil {
		writeUIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// ponytail: re-folds the whole log per request, as `aoa wait` does per
	// poll; fold incrementally from LastSeq if a large log makes it noticeable.
	v, _, err := statusView(events, s.cfg.Pricing, dayBudget(s.cfg))
	if err != nil {
		writeUIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeUIJSON(w, http.StatusOK, v)
}

func (s *uiServer) goalEventsHandler(w http.ResponseWriter, r *http.Request) {
	events, err := s.led.Read()
	if err != nil {
		writeUIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	st, err := state.Fold(events)
	if err != nil {
		writeUIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	id := r.PathValue("id")
	if st.Goals[id] == nil {
		writeUIError(w, http.StatusNotFound, fmt.Sprintf("unknown goal %q", id))
		return
	}
	mine, err := goalEvents(events, st, id)
	if err != nil {
		writeUIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeUIJSON(w, http.StatusOK, mine)
}

// goalEvents picks, in log order, the events that belong to one Goal: those
// naming it by goal_id, and those naming one of its tasks by ticket_id. Events
// that belong to no Goal — a workspace budget, a snapshot — are left out.
func goalEvents(events []api.Event, s *state.State, goalID string) ([]api.Event, error) {
	out := []api.Event{}
	for _, e := range events {
		if len(e.Payload) == 0 {
			continue
		}
		var ref struct {
			GoalID   string `json:"goal_id"`
			TicketID string `json:"ticket_id"`
		}
		if err := json.Unmarshal(e.Payload, &ref); err != nil {
			return nil, fmt.Errorf("event %d: %w", e.Seq, err)
		}
		t := s.Tickets[ref.TicketID]
		if ref.GoalID == goalID || (t != nil && t.GoalID == goalID) {
			out = append(out, e)
		}
	}
	return out, nil
}

// stream is `aoa events --json --since N --follow` as Server-Sent Events: each
// Event Log line, byte for byte, with its seq as the event id. A browser that
// reconnects sends the last id it saw as Last-Event-ID and resumes after it,
// which takes precedence over ?since.
func (s *uiServer) stream(w http.ResponseWriter, r *http.Request) {
	since := 0
	cursor := r.URL.Query().Get("since")
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		cursor = id
	}
	if cursor != "" {
		n, err := strconv.Atoi(cursor)
		if err != nil || n < 0 {
			writeUIError(w, http.StatusBadRequest, fmt.Sprintf("since takes a seq, 0 or more; got %q", cursor))
			return
		}
		since = n
	}
	rc := http.NewResponseController(w)
	// The server's WriteTimeout would cut a long-lived stream; lift it here.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeUIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if _, err := fmt.Fprint(w, "retry: 2000\n\n"); err != nil {
		return
	}
	// Flush now: the browser reports the stream open only once headers
	// arrive, and on a quiet log nothing else would send them for
	// uiKeepAlive.
	if err := rc.Flush(); err != nil {
		return
	}
	quiet := time.Now()
	err := tailLog(r.Context(), s.led, since, s.poll, func(lines []ledger.RawLine) error {
		for _, l := range lines {
			if _, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", l.Event.Seq, l.Bytes); err != nil {
				return err
			}
		}
		switch {
		case len(lines) > 0:
			quiet = time.Now()
		case time.Since(quiet) >= uiKeepAlive:
			quiet = time.Now()
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return err
			}
		default:
			return nil
		}
		return rc.Flush()
	})
	if err != nil && r.Context().Err() == nil {
		// Headers are gone, so all that is left is to end the stream; the
		// browser reconnects and resumes from the last id it received.
		log.Printf("ui: event stream: %v", err)
	}
}

// submitRequest is the body of POST /api/goals.
type submitRequest struct {
	Text string `json:"text"`
	Ref  string `json:"ref"`
	Key  string `json:"key"`
}

func (s *uiServer) submit(r *http.Request) (any, int, error) {
	var req submitRequest
	if err := decodeUIBody(r, &req); err != nil {
		return nil, http.StatusBadRequest, err
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return nil, http.StatusBadRequest, errors.New("goal text is required")
	}
	res, err := submitGoal(s.led, goalRequest{
		Text: text, Source: "ui", Ref: strings.TrimSpace(req.Ref), Key: strings.TrimSpace(req.Key), By: s.by,
	})
	if err != nil {
		return nil, http.StatusConflict, err
	}
	return res, 0, nil
}

func (s *uiServer) amend(r *http.Request) (any, int, error) {
	var req struct {
		Guidance string `json:"guidance"`
	}
	if err := decodeUIBody(r, &req); err != nil {
		return nil, http.StatusBadRequest, err
	}
	guidance := strings.TrimSpace(req.Guidance)
	if guidance == "" {
		return nil, http.StatusBadRequest, errors.New("amendment guidance is required")
	}
	res, err := amendGoal(s.led, r.PathValue("id"), guidance)
	if err != nil {
		return nil, http.StatusConflict, err
	}
	return res, 0, nil
}

func (s *uiServer) cancel(r *http.Request) (any, int, error) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decodeUIBody(r, &req); err != nil {
		return nil, http.StatusBadRequest, err
	}
	res, err := cancelGoal(s.led, cancelRequest{GoalID: r.PathValue("id"), By: s.by, Reason: strings.TrimSpace(req.Reason)})
	if err != nil {
		return nil, http.StatusConflict, err
	}
	return res, 0, nil
}

func (s *uiServer) decide(approve bool) func(*http.Request) (any, int, error) {
	return func(r *http.Request) (any, int, error) {
		var req struct {
			Reason string `json:"reason"`
		}
		if err := decodeUIBody(r, &req); err != nil {
			return nil, http.StatusBadRequest, err
		}
		res, err := decideTicket(s.led, decisionRequest{
			TicketID: r.PathValue("id"), Approve: approve, By: s.by, Reason: strings.TrimSpace(req.Reason),
		})
		if err != nil {
			return nil, http.StatusConflict, err
		}
		return res, 0, nil
	}
}

// decodeUIBody decodes a write's JSON body into v. An empty body is an empty
// object, so a verb with only optional fields can be posted with none.
func decodeUIBody(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("malformed request body: %w", err)
	}
	return nil
}

// writeUIJSON writes v as one JSON line, as the CLI's --json output does.
func writeUIJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status line is already sent; the client sees a truncated body.
		log.Printf("ui: write response: %v", err)
	}
}

// writeUIError writes {"error": msg} with the given status.
func writeUIError(w http.ResponseWriter, code int, msg string) {
	writeUIJSON(w, code, struct {
		Error string `json:"error"`
	}{msg})
}
