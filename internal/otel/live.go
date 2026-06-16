package otel

import (
	"context"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Live streams events to OTLP spans *as they are appended*, so a goal → ticket →
// attempt trace fills in during an `aoa run` rather than only after it. It is the
// streaming counterpart of Export (post-hoc), built on the same span model.
//
// Use it via the ledger append hook: Seed the existing log to open spans for any
// in-flight work (backdated to the real timestamps), register Observe as the
// hook, then Shutdown closes whatever is still open and flushes. It is off by
// default — NewLive returns (nil, nil) unless Enabled(); every method is a no-op
// on a nil receiver, so the hermetic suite never networks.
type Live struct {
	mu      sync.Mutex
	tp      *sdktrace.TracerProvider
	tr      trace.Tracer
	goals   map[string]liveGoal
	tickets map[string]*liveTicket
}

type liveGoal struct {
	ctx  context.Context
	span trace.Span
}

type liveTicket struct {
	ctx      context.Context
	span     trace.Span
	attempt  trace.Span // the open attempt span, or nil
	attempts int
}

// NewLive builds a streaming exporter, or returns (nil, nil) when no OTLP
// endpoint is configured (the off switch).
func NewLive(ctx context.Context) (*Live, error) {
	if !Enabled() {
		return nil, nil
	}
	res, err := newResource(ctx)
	if err != nil {
		return nil, err
	}
	texp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("otlp trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(texp), sdktrace.WithResource(res))
	return &Live{
		tp:      tp,
		tr:      tp.Tracer("aoa"),
		goals:   map[string]liveGoal{},
		tickets: map[string]*liveTicket{},
	}, nil
}

// Seed replays an existing log through the state machine with the events' own
// timestamps: already-finished work opens and closes (so it shows real
// durations), in-flight work stays open to be continued live by Observe.
func (l *Live) Seed(events []api.Event) {
	if l == nil {
		return
	}
	for _, e := range events {
		l.observe(e, false)
	}
}

// Observe handles one freshly-appended event, opening/closing spans in real time.
// Safe to register directly as ledger.SetAppendHook.
func (l *Live) Observe(e api.Event) { l.observe(e, true) }

// Shutdown ends any spans still open (the run is over) and flushes the exporter.
func (l *Live) Shutdown(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	for _, t := range l.tickets {
		if t.attempt != nil {
			t.attempt.End()
		}
		t.span.End()
	}
	for _, g := range l.goals {
		g.span.End()
	}
	l.tickets = map[string]*liveTicket{}
	l.goals = map[string]liveGoal{}
	l.mu.Unlock()
	return l.tp.Shutdown(ctx)
}

// observe is the shared state machine. live ⇒ spans use wall-clock now; otherwise
// (seeding) they are backdated to the event's timestamp.
func (l *Live) observe(e api.Event, live bool) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	// startOpts/endOpts carry the timestamp only when seeding historical events.
	startOpts := func(extra ...trace.SpanStartOption) []trace.SpanStartOption {
		if !live {
			extra = append(extra, trace.WithTimestamp(e.Timestamp))
		}
		return extra
	}
	endOpts := func() []trace.SpanEndOption {
		if live {
			return nil
		}
		return []trace.SpanEndOption{trace.WithTimestamp(e.Timestamp)}
	}

	switch e.Type {
	case api.GoalSubmitted:
		var p api.GoalSubmittedPayload
		if e.DecodePayload(&p) != nil {
			return
		}
		if _, ok := l.goals[p.GoalID]; ok {
			return
		}
		ctx, span := l.tr.Start(context.Background(), "goal "+p.GoalID,
			startOpts(trace.WithAttributes(
				attribute.String("aoa.goal.id", p.GoalID),
				attribute.String("aoa.goal.text", p.Text),
			))...)
		l.goals[p.GoalID] = liveGoal{ctx: ctx, span: span}

	case api.TicketCreated:
		var p api.TicketCreatedPayload
		if e.DecodePayload(&p) != nil || l.tickets[p.TicketID] != nil {
			return
		}
		parent := context.Background()
		if g, ok := l.goals[p.GoalID]; ok {
			parent = g.ctx
		}
		ctx, span := l.tr.Start(parent, "ticket "+p.TicketID,
			startOpts(trace.WithAttributes(
				attribute.String("aoa.ticket.id", p.TicketID),
				attribute.String("aoa.ticket.title", p.Title),
			))...)
		l.tickets[p.TicketID] = &liveTicket{ctx: ctx, span: span}

	case api.WorkStarted:
		t := l.tickets[e.TicketID()]
		if t == nil {
			return
		}
		t.attempts++
		_, t.attempt = l.tr.Start(t.ctx, fmt.Sprintf("attempt %d (%s)", t.attempts, e.TicketID()),
			startOpts(trace.WithAttributes(attribute.Int("aoa.attempt", t.attempts)))...)

	case api.ProposalSubmitted:
		t := l.tickets[e.TicketID()]
		if t == nil {
			return
		}
		var p api.ProposalSubmittedPayload
		if e.DecodePayload(&p) == nil {
			attrs := []attribute.KeyValue{attribute.Int("aoa.tokens", p.Tokens), attribute.String("aoa.model", p.Model)}
			t.span.SetAttributes(attrs...)
			if t.attempt != nil {
				t.attempt.SetAttributes(attrs...)
			}
		}

	case api.VerificationFailed:
		t := l.tickets[e.TicketID()]
		if t == nil || t.attempt == nil {
			return
		}
		var p api.VerificationFailedPayload
		_ = e.DecodePayload(&p)
		t.attempt.SetStatus(codes.Error, p.Reason)
		t.attempt.End(endOpts()...)
		t.attempt = nil

	case api.Merged:
		l.endTicket(e.TicketID(), endOpts(), "", false)

	case api.TicketFailed:
		var p api.TicketFailedPayload
		_ = e.DecodePayload(&p)
		l.endTicket(e.TicketID(), endOpts(), p.Reason, true)

	case api.TicketDecomposed:
		l.endTicket(e.TicketID(), endOpts(), "", false)
	}
}

// endTicket closes a ticket's open attempt (if any) and the ticket span. When
// failed, both are marked Error with reason. Caller holds l.mu.
func (l *Live) endTicket(tid string, opts []trace.SpanEndOption, reason string, failed bool) {
	t := l.tickets[tid]
	if t == nil {
		return
	}
	if t.attempt != nil {
		if failed {
			t.attempt.SetStatus(codes.Error, reason)
		}
		t.attempt.End(opts...)
		t.attempt = nil
	}
	if failed {
		t.span.SetStatus(codes.Error, reason)
	}
	t.span.End(opts...)
	delete(l.tickets, tid)
}
