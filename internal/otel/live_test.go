package otel

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	coltrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
	"github.com/stretchr/testify/require"
)

func TestNewLiveNilWhenDisabled(t *testing.T) {
	l, err := NewLive(context.Background())
	if err != nil {
		t.Fatalf("NewLive: %v", err)
	}
	if l != nil {
		t.Fatal("NewLive should be nil with no OTLP endpoint set")
	}
	// Nil-receiver methods must be safe no-ops.
	l.Seed([]api.Event{})
	l.Observe(api.Event{})
	if err := l.Shutdown(context.Background()); err != nil {
		t.Fatalf("nil Shutdown: %v", err)
	}
}

func TestLiveStreamsSpanTree(t *testing.T) {
	var (
		mu    sync.Mutex
		names []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		if r.URL.Path == "/v1/traces" {
			var req coltrace.ExportTraceServiceRequest
			if proto.Unmarshal(body, &req) == nil {
				mu.Lock()
				for _, rs := range req.ResourceSpans {
					for _, ss := range rs.ScopeSpans {
						for _, sp := range ss.Spans {
							names = append(names, sp.Name)
						}
					}
				}
				mu.Unlock()
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)

	live, err := NewLive(context.Background())
	if err != nil || live == nil {
		t.Fatalf("NewLive: live=%v err=%v", live, err)
	}

	// Seed a goal that already exists, then stream the rest of the work live.
	s := newStream(t)
	s.add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "build"})
	live.Seed(s.evs)
	for _, e := range []struct {
		typ api.EventType
		pl  any
	}{
		{api.TicketCreated, api.TicketCreatedPayload{TicketID: "t1", GoalID: "g1", Title: "do it"}},
		{api.WorkStarted, api.WorkStartedPayload{TicketID: "t1", Worker: "w1"}},
		{api.ProposalSubmitted, api.ProposalSubmittedPayload{TicketID: "t1", Tokens: 10, Model: "grok"}},
		{api.Merged, api.MergedPayload{TicketID: "t1"}},
	} {
		ev, err := api.NewEvent(e.typ, "test", e.pl)
		require.NoError(t, err)
		live.Observe(ev)
	}
	if err := live.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{"goal g1": false, "ticket t1": false, "attempt 1 (t1)": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("missing span %q; got %v", name, names)
		}
	}
}

// The live projection must drop a redelivered keyed submit exactly as the
// post-hoc one does: same idempotency key, new Goal id, no second span. Issue
// #132 named only the post-hoc projection; the same gap was here.
func TestLiveDedupesGoalsByIdempotencyKey(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1") // nothing is exported; spans are counted in-process
	live, err := NewLive(context.Background())
	require.NoError(t, err)
	require.NotNil(t, live)
	t.Cleanup(func() {
		if err := live.Shutdown(context.Background()); err != nil {
			t.Logf("shutdown: %v", err)
		}
	})

	s := newStream(t)
	s.add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g1", Text: "build", IdempotencyKey: "gh:1"})
	s.add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g2", Text: "build", IdempotencyKey: "gh:1"})
	s.add(api.GoalSubmitted, api.GoalSubmittedPayload{GoalID: "g3", Text: "other"})
	for _, e := range s.evs {
		live.Observe(e)
	}

	require.Len(t, live.goals, 2, "a redelivery opens no second goal span")
	require.Contains(t, live.goals, "g1")
	require.Contains(t, live.goals, "g3")
	require.NotContains(t, live.goals, "g2")
}
