package telemetry

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	psql "github.com/epzero/basamento/platform/sql"
)

func newFakeClockAt(at time.Time) *coretest.FakeClock {
	return coretest.NewFakeClock(at)
}

func newTestSQLStore(t *testing.T) *psql.Store {
	t.Helper()
	migrations := fstest.MapFS{
		"00001_noop.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n\n-- +goose Down\nSELECT 1;\n")},
	}
	store, err := psql.Open(t.Context(), "sqlite:///"+filepath.Join(t.TempDir(), "telemetry.db"), migrations)
	if err != nil {
		t.Fatalf("open SQL store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newTestSpanStore(t *testing.T) *SQLSpanStore {
	t.Helper()
	spans, err := NewSQLSpanStore(t.Context(), newTestSQLStore(t), 0)
	if err != nil {
		t.Fatalf("NewSQLSpanStore: %v", err)
	}
	return spans
}

// TestConfigSecretsAreRedactedFromEverySignal covers the wiring of
// Config.Secrets into the redactor: the value must be replaced in a kept log
// record, a span attribute and a metric label.
func TestConfigSecretsAreRedactedFromEverySignal(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	setup, err := New(Config{
		Profile: ProfileEmbedded,
		Clock:   clock,
		Spans:   newTestSpanStore(t),
		Secrets: NewStaticSecrets("sk-live-7f3a9c1b"),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = setup.Shutdown(context.WithoutCancel(t.Context())) })

	ctx := telemetryContext("acme", "run_01")
	setup.Logger().InfoContext(ctx, "key sk-live-7f3a9c1b in use")
	ctx, span := setup.Tracer().Start(ctx, "step a")
	span.SetAttributes(attribute.String("note", "sk-live-7f3a9c1b"))
	span.End()
	counter, _ := setup.Meter().Int64Counter("runs.started")
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("note", "sk-live-7f3a9c1b")))

	if got := setup.Logs()[0].Message; got != Redacted {
		t.Errorf("log message = %q, want redacted", got)
	}
	kept, ok := setup.SpanByName("step a")
	if !ok {
		t.Fatal("span not kept")
	}
	if got := kept.Attributes["note"]; got != Redacted {
		t.Errorf("span attribute = %v, want redacted", got)
	}
	points := setup.Metrics()
	if len(points) != 1 || points[0].Labels["note"] != Redacted {
		t.Errorf("metric labels = %v, want the external credential redacted", points)
	}
}

// TestSetupAppliesConfiguredRetentionOverTheStore proves the setup's retention,
// not the value the store was built with, is the effective purge window.
func TestSetupAppliesConfiguredRetentionOverTheStore(t *testing.T) {
	base := time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)
	clock := newFakeClockAt(base)

	t.Run("configured period wins over a longer store period", func(t *testing.T) {
		store, err := NewSQLSpanStore(t.Context(), newTestSQLStore(t), 48*time.Hour)
		if err != nil {
			t.Fatalf("NewSQLSpanStore: %v", err)
		}
		setup, err := New(Config{Profile: ProfileEmbedded, Clock: clock, Spans: store, RetentionPeriod: time.Hour})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ctx := tenantRun("acme", "run_01")
		for _, span := range []SpanData{
			{TraceID: "t1", SpanID: "sid-old", Name: "old", Start: base.Add(-4 * time.Hour), End: base.Add(-3 * time.Hour), Tenant: "acme", RunID: "run_01"},
			{TraceID: "t1", SpanID: "sid-new", Name: "new", Start: base.Add(-time.Hour), End: base.Add(-30 * time.Minute), Tenant: "acme", RunID: "run_01"},
		} {
			if err := store.Append(ctx, span); err != nil {
				t.Fatalf("Append: %v", err)
			}
		}
		n, err := setup.PurgeSpans(ctx)
		if err != nil {
			t.Fatalf("PurgeSpans: %v", err)
		}
		if n != 1 {
			t.Errorf("purged %d spans, want 1 (the 3-hour-old one)", n)
		}
	})

	t.Run("zero means the default period", func(t *testing.T) {
		store, err := NewSQLSpanStore(t.Context(), newTestSQLStore(t), 48*time.Hour)
		if err != nil {
			t.Fatalf("NewSQLSpanStore: %v", err)
		}
		setup, err := New(Config{Profile: ProfileEmbedded, Clock: clock, Spans: store})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ctx := tenantRun("acme", "run_01")
		if err := store.Append(ctx, SpanData{
			TraceID: "t1", SpanID: "sid-old", Name: "old",
			Start: base.Add(-26 * time.Hour), End: base.Add(-25 * time.Hour),
			Tenant: "acme", RunID: "run_01",
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
		n, err := setup.PurgeSpans(ctx)
		if err != nil {
			t.Fatalf("PurgeSpans: %v", err)
		}
		if n != 1 {
			t.Errorf("purged %d spans, want 1 (DefaultRetentionPeriod applies)", n)
		}
	})
}

// TestKeptSignalsAreDeepCopied proves a caller mutating a record returned by
// Logs, Metrics or Spans cannot corrupt the kept signal.
func TestKeptSignalsAreDeepCopied(t *testing.T) {
	setup := newEmbeddedTestSetup(t, coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)))
	ctx := telemetryContext("acme", "run_01")
	setup.Logger().InfoContext(ctx, "kept", slog.Group("group", slog.String("key", "value")))
	_, span := setup.Tracer().Start(ctx, "step a")
	span.SetAttributes(attribute.String("note", "kept"))
	span.End()
	counter, _ := setup.Meter().Int64Counter("runs.started")
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("note", "kept")))

	logs := setup.Logs()
	logs[0].Attrs["injected"] = "x"
	logs[0].Attrs["group"].(map[string]any)["key"] = "mutated"
	if _, ok := setup.Logs()[0].Attrs["injected"]; ok {
		t.Error("a mutated log Attrs map is shared with the kept record")
	}
	if got := setup.Logs()[0].Attrs["group"].(map[string]any)["key"]; got != "value" {
		t.Errorf("nested log attribute = %v, want value", got)
	}

	spans := setup.Spans()
	spans[0].Attributes["injected"] = "x"
	if _, ok := setup.Spans()[0].Attributes["injected"]; ok {
		t.Error("a mutated span Attributes map is shared with the kept span")
	}

	metrics := setup.Metrics()
	metrics[0].Labels["injected"] = "x"
	if _, ok := setup.Metrics()[0].Labels["injected"]; ok {
		t.Error("a mutated metric Labels map is shared with the kept point")
	}
}

func newEmbeddedTestSetup(t *testing.T, clock core.Clock) *Setup {
	t.Helper()
	setup, err := New(Config{
		Profile: ProfileEmbedded,
		Clock:   clock,
		Spans:   newTestSpanStore(t),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		_ = setup.Shutdown(context.WithoutCancel(t.Context()))
	})
	return setup
}
