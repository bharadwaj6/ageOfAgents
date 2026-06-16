package otel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/bharadwaj6/ageOfAgents/internal/diagnose"
	"github.com/bharadwaj6/ageOfAgents/internal/metrics"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
	"github.com/stretchr/testify/require"
)

// stream is a tiny event-log builder with a monotonic clock, mirroring the one
// in internal/metrics so spans get realistic, ordered timestamps.
type stream struct {
	t     *testing.T
	evs   []api.Event
	n     int
	clock time.Time
}

func newStream(t *testing.T) *stream {
	return &stream{t: t, clock: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (s *stream) add(typ api.EventType, payload any) *stream {
	s.t.Helper()
	e, err := api.NewEvent(typ, "test", payload)
	if err != nil {
		s.t.Fatalf("NewEvent: %v", err)
	}
	s.n++
	e.Seq = s.n
	s.clock = s.clock.Add(time.Second)
	e.Timestamp = s.clock
	s.evs = append(s.evs, e)
	return s
}

// mergedHistory is a goal whose single ticket goes WorkStarted → Proposal →
// Merged: one goal span, one ticket span, one attempt span.
func mergedHistory(t *testing.T) []api.Event {
	return newStream(t).
		add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "build a thing"}).
		add(api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "do it"}).
		add(api.TicketReady, api.TicketReadyPayload{TicketID: "t1"}).
		add(api.WorkStarted, api.WorkStartedPayload{TicketID: "t1", Worker: "w1"}).
		add(api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "t1", Worker: "w1", Tokens: 1200, Model: "grok"}).
		add(api.VerificationPassed, api.VerificationPassedPayload{TicketID: "t1", Worker: "w1"}).
		add(api.Merged, api.MergedPayload{TicketID: "t1", Worker: "w1"}).
		evs
}

func TestEnabledTracksEndpointEnv(t *testing.T) {
	if Enabled() {
		t.Fatal("Enabled() should be false with no OTLP env set")
	}
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:4318")
	if !Enabled() {
		t.Fatal("Enabled() should be true once the endpoint is set")
	}
}

func TestExportNoopWhenDisabled(t *testing.T) {
	// No OTLP env set: Export must return nil and never touch the network. If it
	// tried, this hermetic test would hang or fail — that is the guarantee.
	events := mergedHistory(t)
	if err := Export(context.Background(), events, metrics.Compute(events), diagnose.Classify(events), nil); err != nil {
		t.Fatalf("disabled Export should be a no-op, got %v", err)
	}
}

func TestExportEmitsSpanTree(t *testing.T) {
	var (
		mu        sync.Mutex
		spanNames []string
		gotMetric bool
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		switch r.URL.Path {
		case "/v1/traces":
			var req coltrace.ExportTraceServiceRequest
			if err := proto.Unmarshal(body, &req); err == nil {
				mu.Lock()
				for _, rs := range req.ResourceSpans {
					for _, ss := range rs.ScopeSpans {
						for _, sp := range ss.Spans {
							spanNames = append(spanNames, sp.Name)
						}
					}
				}
				mu.Unlock()
			}
		case "/v1/metrics":
			mu.Lock()
			gotMetric = len(body) > 0
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)

	events := mergedHistory(t)
	if err := Export(context.Background(), events, metrics.Compute(events), diagnose.Classify(events), map[string]float64{"grok": 5.0}); err != nil {
		t.Fatalf("Export: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{"goal g1": false, "ticket t1": false, "attempt 1 (t1)": false}
	for _, n := range spanNames {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing span %q; got %v", name, spanNames)
		}
	}
	if !gotMetric {
		t.Error("no metrics export received at /v1/metrics")
	}
}
