package telemetry

import (
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/epzero/basamento/core/coretest"
)

func TestSpanTimesComeFromClock(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	setup := newEmbeddedTestSetup(t, clock)

	ctx, span := setup.Tracer().Start(telemetryContext("acme", "run_01"), "step a")
	clock.Advance(250 * time.Millisecond)
	span.End()

	kept, ok := setup.SpanByName("step a")
	if !ok {
		t.Fatal("span not kept")
	}
	if !kept.Start.Equal(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("start = %v", kept.Start)
	}
	if !kept.End.Equal(time.Date(2026, 10, 1, 9, 0, 0, 250_000_000, time.UTC)) {
		t.Errorf("end = %v", kept.End)
	}
	if kept.Duration() != 250*time.Millisecond {
		t.Errorf("duration = %v", kept.Duration())
	}
	if trace.SpanFromContext(ctx) != span {
		t.Error("started span is not the active span of the returned context")
	}
}

func TestSpanKeepsParentStatusRunAndTenant(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	setup := newEmbeddedTestSetup(t, clock)

	parentCtx, parent := setup.Tracer().Start(telemetryContext("acme", "run_01"), "run_01")
	childCtx, child := setup.Tracer().Start(parentCtx, "step a")
	child.SetStatus(codes.Error, "boom")
	child.End()
	parent.End()

	kept, ok := setup.SpanByName("step a")
	if !ok {
		t.Fatal("child span not kept")
	}
	if kept.TraceID != parent.SpanContext().TraceID().String() {
		t.Errorf("trace id = %s, want %s", kept.TraceID, parent.SpanContext().TraceID())
	}
	if kept.ParentSpanID != parent.SpanContext().SpanID().String() {
		t.Errorf("parent span id = %s, want %s", kept.ParentSpanID, parent.SpanContext().SpanID())
	}
	if kept.Status != StatusError {
		t.Errorf("status = %s, want error", kept.Status)
	}
	if kept.RunID != "run_01" || kept.Tenant != "acme" {
		t.Errorf("run/tenant = %q/%q", kept.RunID, kept.Tenant)
	}
	if trace.SpanFromContext(childCtx) != child {
		t.Error("child is not active in its context")
	}
}

func TestTraceContextCarrierRoundTrip(t *testing.T) {
	setup := newEmbeddedTestSetup(t, coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)))

	parentCtx, parent := setup.Tracer().Start(telemetryContext("acme", "run_01"), "run_01")
	carrier := propagation.MapCarrier{}
	setup.Inject(parentCtx, carrier)
	restored := setup.Extract(telemetryContext("acme", "run_01"), carrier)

	_, child := setup.Tracer().Start(restored, "step a")
	child.End()
	parent.End()

	kept, ok := setup.SpanByName("step a")
	if !ok {
		t.Fatal("child span not kept")
	}
	if kept.TraceID != parent.SpanContext().TraceID().String() {
		t.Errorf("trace id = %s, want %s", kept.TraceID, parent.SpanContext().TraceID())
	}
	if kept.ParentSpanID != parent.SpanContext().SpanID().String() {
		t.Errorf("parent = %s, want %s", kept.ParentSpanID, parent.SpanContext().SpanID())
	}
}

func TestShutdownStopsSpans(t *testing.T) {
	setup := newEmbeddedTestSetup(t, coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)))
	if err := setup.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	ctx, span := setup.Tracer().Start(telemetryContext("acme", "run_01"), "late")
	span.End()
	if got := len(setup.Spans()); got != 0 {
		t.Errorf("kept %d spans after shutdown, want 0", got)
	}
	if trace.SpanFromContext(ctx).IsRecording() {
		t.Error("span started after shutdown is recording")
	}
}
