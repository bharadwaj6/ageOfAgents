// Package otel projects a finished Event Log into OpenTelemetry traces and
// metrics. It is observability as a *replay projection*, not bespoke
// instrumentation threaded through the hot path: like metrics.Compute and
// diagnose.Classify, Export is a pure function of the event slice plus the
// already-computed views (see docs/design/adr/012-observability-as-replay-projection.md).
//
// It is off by default. Export is a no-op unless an OTLP endpoint is configured
// via the standard OTEL_EXPORTER_OTLP_ENDPOINT env var, so the hermetic test
// suite never opens a network connection. Because it speaks plain OTLP over the
// standard env vars, it is vendor-agnostic: Honeycomb, Grafana Tempo, Datadog,
// Jaeger, or an OTel Collector are all just an endpoint + headers.
package otel

import (
	"context"
	"errors"
	"fmt"
	"os"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/bharadwaj6/ageOfAgents/internal/diagnose"
	"github.com/bharadwaj6/ageOfAgents/internal/metrics"
	"github.com/bharadwaj6/ageOfAgents/pkg/api"
)

// Enabled reports whether an OTLP endpoint is configured. When false, Export is
// a no-op — this is the single off switch that keeps offline runs (and the
// hermetic test suite) from ever reaching the network.
func Enabled() bool {
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") != ""
}

// Export ships the event history as OTLP traces (a goal → ticket → attempt span
// tree, with each event riding as a span event) and the computed views as OTLP
// metrics, then flushes and shuts the exporters down. price (USD per million
// tokens, keyed by model) adds a cost gauge when non-empty. Extra resource
// attributes (e.g. a per-task service.name for an eval instance) override the
// defaults. It returns nil immediately when Enabled() is false.
func Export(ctx context.Context, events []api.Event, m metrics.Metrics, d diagnose.Report, price map[string]float64, extra ...attribute.KeyValue) error {
	if !Enabled() {
		return nil
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(attribute.String("service.name", "aoa")), // default; OTEL_SERVICE_NAME overrides
		resource.WithFromEnv(),
		resource.WithAttributes(extra...), // caller (e.g. per eval task) wins
	)
	if err != nil {
		return fmt.Errorf("otel resource: %w", err)
	}

	texp, err := otlptracehttp.New(ctx) // reads OTEL_EXPORTER_OTLP_* from env
	if err != nil {
		return fmt.Errorf("otlp trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(texp), sdktrace.WithResource(res))

	mexp, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return fmt.Errorf("otlp metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(mexp)))

	emitTraces(ctx, tp.Tracer("aoa"), events)
	emitMetrics(mp.Meter("aoa"), m, d, price)

	// Flush both pipelines and shut down; ForceFlush is what actually pushes the
	// PeriodicReader's metrics and the batched spans out over OTLP.
	return errors.Join(
		tp.ForceFlush(ctx), tp.Shutdown(ctx),
		mp.ForceFlush(ctx), mp.Shutdown(ctx),
	)
}

// ticketAgg collects one ticket's goal, title, and its events in append order.
type ticketAgg struct {
	goalID string
	title  string
	evs    []api.Event
}

// emitTraces builds the span tree from the log: one span per goal, a child span
// per ticket, and a grandchild span per attempt (WorkStarted → terminal). Spans
// are backdated to the events' own timestamps so the trace reflects the real run.
func emitTraces(ctx context.Context, tr trace.Tracer, events []api.Event) {
	goalText := map[string]string{}
	goalStart := map[string]api.Event{}
	var goalOrder []string
	tickets := map[string]*ticketAgg{}
	var ticketOrder []string

	for _, e := range events {
		switch e.Type {
		case api.GoalSubmitted:
			var p api.GoalSubmittedPayload
			if e.DecodePayload(&p) == nil {
				if _, ok := goalStart[p.GoalID]; !ok {
					goalOrder = append(goalOrder, p.GoalID)
				}
				goalStart[p.GoalID] = e
				goalText[p.GoalID] = p.Text
			}
		case api.TicketCreated:
			var p api.TicketCreatedPayload
			if e.DecodePayload(&p) == nil && tickets[p.TicketID] == nil {
				tickets[p.TicketID] = &ticketAgg{goalID: p.GoalID, title: p.Title}
				ticketOrder = append(ticketOrder, p.TicketID)
			}
		}
	}

	// Assign every ticket-scoped event to its ticket, and track each goal's last
	// activity (so the goal span ends when its work does).
	goalEnd := map[string]api.Event{}
	for gid, e := range goalStart {
		goalEnd[gid] = e
	}
	for _, e := range events {
		t := tickets[e.TicketID()]
		if t == nil {
			continue
		}
		t.evs = append(t.evs, e)
		if last, ok := goalEnd[t.goalID]; !ok || e.Timestamp.After(last.Timestamp) {
			goalEnd[t.goalID] = e
		}
	}

	for _, gid := range goalOrder {
		gctx, gspan := tr.Start(ctx, "goal "+gid,
			trace.WithTimestamp(goalStart[gid].Timestamp),
			trace.WithAttributes(
				attribute.String("aoa.goal.id", gid),
				attribute.String("aoa.goal.text", goalText[gid]),
			))
		for _, tid := range ticketOrder {
			t := tickets[tid]
			if t.goalID != gid || len(t.evs) == 0 {
				continue
			}
			emitTicket(gctx, tr, tid, t)
		}
		gspan.End(trace.WithTimestamp(goalEnd[gid].Timestamp))
	}
}

func emitTicket(ctx context.Context, tr trace.Tracer, tid string, t *ticketAgg) {
	start := t.evs[0].Timestamp
	end := t.evs[len(t.evs)-1].Timestamp
	tctx, span := tr.Start(ctx, "ticket "+tid,
		trace.WithTimestamp(start),
		trace.WithAttributes(
			attribute.String("aoa.ticket.id", tid),
			attribute.String("aoa.ticket.title", t.title),
		))

	var aspan trace.Span // the open attempt span, if any
	attempt := 0
	for _, e := range t.evs {
		switch e.Type {
		case api.WorkStarted:
			attempt++
			_, aspan = tr.Start(tctx, fmt.Sprintf("attempt %d (%s)", attempt, tid),
				trace.WithTimestamp(e.Timestamp),
				trace.WithAttributes(attribute.Int("aoa.attempt", attempt)))
		case api.ProposalSubmitted:
			var p api.ProposalSubmittedPayload
			if e.DecodePayload(&p) == nil {
				attrs := []attribute.KeyValue{attribute.Int("aoa.tokens", p.Tokens), attribute.String("aoa.model", p.Model)}
				span.SetAttributes(attrs...)
				if aspan != nil {
					aspan.SetAttributes(attrs...)
				}
			}
		case api.VerificationFailed:
			if aspan != nil {
				var p api.VerificationFailedPayload
				_ = e.DecodePayload(&p)
				aspan.SetStatus(codes.Error, p.Reason)
				aspan.End(trace.WithTimestamp(e.Timestamp))
				aspan = nil
			}
		case api.Merged:
			if aspan != nil {
				aspan.End(trace.WithTimestamp(e.Timestamp))
				aspan = nil
			}
		case api.TicketFailed:
			var p api.TicketFailedPayload
			_ = e.DecodePayload(&p)
			span.SetStatus(codes.Error, p.Reason)
			if aspan != nil {
				aspan.SetStatus(codes.Error, p.Reason)
				aspan.End(trace.WithTimestamp(e.Timestamp))
				aspan = nil
			}
		}
		span.AddEvent(string(e.Type), trace.WithTimestamp(e.Timestamp))
	}
	if aspan != nil { // never resolved (e.g. log ends mid-attempt)
		aspan.End(trace.WithTimestamp(end))
	}
	span.End(trace.WithTimestamp(end))
}

// emitMetrics publishes the computed views as OTLP gauges under the aoa.* name
// space. One Record per instrument; a single export of the finished run.
func emitMetrics(mt metric.Meter, m metrics.Metrics, d diagnose.Report, price map[string]float64) {
	ctx := context.Background()
	setI := func(name string, v int64) {
		if g, err := mt.Int64Gauge("aoa." + name); err == nil {
			g.Record(ctx, v)
		}
	}
	setF := func(name string, v float64) {
		if g, err := mt.Float64Gauge("aoa." + name); err == nil {
			g.Record(ctx, v)
		}
	}

	setI("goals", int64(m.Goals))
	setI("tickets", int64(m.Tickets))
	setI("merged", int64(m.Merged))
	setI("failed", int64(m.Failed))
	setI("decomposed", int64(m.Decomposed))
	setI("tokens_total", int64(m.TokensTotal))
	setI("regression_escapes", int64(m.RegressionEscapes))
	setI("merge_queue_max_depth", int64(m.MergeQueueMaxDepth))
	setF("merge_queue_wait_mean_seconds", m.MergeQueueWaitMean)
	setF("merge_queue_wait_max_seconds", m.MergeQueueWaitMax)
	setF("merge_correctness", m.MergeCorrectness)
	setF("rejected_proposal_rate", m.RejectedProposalRate)
	setF("regression_escape_rate", m.RegressionEscapeRate)
	setF("throughput_per_min", m.ThroughputPerMin)
	setF("duration_seconds", m.DurationSeconds)
	if len(price) > 0 {
		setF("cost_usd", metrics.USD(m.TokensByModel, price))
	}

	if g, err := mt.Int64Gauge("aoa.tokens_by_model"); err == nil {
		for model, toks := range m.TokensByModel {
			g.Record(ctx, int64(toks), metric.WithAttributes(attribute.String("model", model)))
		}
	}
	if g, err := mt.Int64Gauge("aoa.failure_mode"); err == nil {
		for _, f := range d.Findings {
			g.Record(ctx, int64(f.Count), metric.WithAttributes(attribute.String("mode", string(f.Mode))))
		}
	}
}
