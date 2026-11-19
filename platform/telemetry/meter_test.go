package telemetry

import (
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/epzero/basamento/core/coretest"
)

func TestMeterRecordsPointFromClock(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	setup := newEmbeddedTestSetup(t, clock)
	counter, err := setup.Meter().Int64Counter("runs.started")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	counter.Add(telemetryContext("acme", "run_01"), 1, metric.WithAttributes(attribute.String("note", "ok")))
	points := setup.Metrics()
	if len(points) != 1 {
		t.Fatalf("kept %d points, want 1", len(points))
	}
	point := points[0]
	if point.Name != "runs.started" || point.Value != 1 || !point.IsInt {
		t.Errorf("point = %+v", point)
	}
	if !point.Time.Equal(clock.Now()) {
		t.Errorf("time = %v, want %v", point.Time, clock.Now())
	}
	if point.Labels["note"] != "ok" {
		t.Errorf("labels = %v", point.Labels)
	}
}

func TestMeterRedactsLabelsBeforeKeeping(t *testing.T) {
	setup := newEmbeddedTestSetup(t, coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)))
	setup.SetCredential("openai", "sk-live-7f3a9c1b")
	counter, err := setup.Meter().Int64Counter("runs")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	counter.Add(telemetryContext("acme", "run_01"), 1, metric.WithAttributes(
		attribute.String("note", "sk-live-7f3a9c1b"),
		attribute.String("password", "hunter2"),
		attribute.Int("token_count", 12),
	))
	labels := setup.Metrics()[0].Labels
	if labels["note"] != Redacted || labels["password"] != Redacted {
		t.Errorf("labels = %v, want redacted values", labels)
	}
	if labels["token_count"] != int64(12) {
		t.Errorf("token_count = %v, want 12", labels["token_count"])
	}
}

func TestShutdownStopsMetrics(t *testing.T) {
	setup := newEmbeddedTestSetup(t, coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)))
	counter, err := setup.Meter().Int64Counter("runs.started")
	if err != nil {
		t.Fatalf("Int64Counter: %v", err)
	}
	if err := setup.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	counter.Add(telemetryContext("acme", "run_01"), 1)
	if got := len(setup.Metrics()); got != 0 {
		t.Errorf("kept %d points after shutdown, want 0", got)
	}
}
