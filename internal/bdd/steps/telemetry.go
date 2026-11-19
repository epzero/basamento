package steps

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing/fstest"
	"time"

	"github.com/cucumber/godog"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/bdd/telemtest"
	psql "github.com/epzero/basamento/platform/sql"
	"github.com/epzero/basamento/platform/telemetry"
)

type telemetrySpan struct {
	ctx  context.Context
	span trace.Span
}

type telemetryWorld struct {
	setup     *telemetry.Setup
	spans     telemetry.SpanStore
	store     *psql.Store
	dataDir   string
	collector *telemtest.Collector

	tenant string
	run    string

	spanRefs   map[string]telemetrySpan
	restored   context.Context
	lastRead   []telemetry.SpanData
	lastErr    error
	before     time.Time
	after      time.Time
	lastRecord error
	shutdownOK int
}

func newTelemetryWorld() *telemetryWorld {
	return &telemetryWorld{spanRefs: map[string]telemetrySpan{}}
}

func (w *telemetryWorld) teardown() {
	if w.collector != nil {
		// A slow collector must answer teardown's final flush; the scenario
		// itself never waits on it.
		w.collector.ReleaseSlow()
	}
	if w.setup != nil {
		_ = w.setup.Shutdown(context.Background())
		w.setup = nil
	}
	if w.collector != nil {
		w.collector.Close()
		w.collector = nil
	}
	if w.store != nil {
		_ = w.store.Close()
		w.store = nil
	}
	if w.dataDir != "" {
		_ = os.RemoveAll(w.dataDir)
		w.dataDir = ""
	}
}

// telemetryClock reads the scenario's fake clock through the core world, so a
// later "the clock is frozen at …" step (which replaces Core.clock) is visible
// to the setup.
type telemetryClock struct{ core *coreWorld }

func (c telemetryClock) Now() time.Time { return c.core.clock.Now() }

func (w *telemetryWorld) startEmbedded(ctx context.Context, coreWorld *coreWorld) error {
	w.teardown()
	dir, err := os.MkdirTemp("", "telemetry-bdd-*")
	if err != nil {
		return err
	}
	w.dataDir = dir
	migrations := fstest.MapFS{
		"00001_noop.sql": &fstest.MapFile{Data: []byte("-- +goose Up\nSELECT 1;\n\n-- +goose Down\nSELECT 1;\n")},
	}
	store, err := psql.Open(ctx, "sqlite:///"+filepath.Join(dir, "telemetry.db"), migrations)
	if err != nil {
		return err
	}
	w.store = store
	spans, err := telemetry.NewSQLSpanStore(ctx, store)
	if err != nil {
		return err
	}
	w.spans = spans
	setup, err := telemetry.New(telemetry.Config{
		Profile:         telemetry.ProfileEmbedded,
		Clock:           telemetryClock{core: coreWorld},
		Spans:           spans,
		RetentionPeriod: 24 * time.Hour,
	})
	if err != nil {
		return err
	}
	w.setup = setup
	return nil
}

func (w *telemetryWorld) startCloud(ctx context.Context, coreWorld *coreWorld) error {
	w.teardown()
	w.collector = telemtest.New()
	setup, err := telemetry.New(telemetry.Config{
		Profile:         telemetry.ProfileCloud,
		CollectorURL:    w.collector.URL(),
		Clock:           telemetryClock{core: coreWorld},
		RetentionPeriod: 24 * time.Hour,
	})
	if err != nil {
		return err
	}
	w.setup = setup
	return nil
}

func (w *telemetryWorld) ctxFor(tenant, run string) context.Context {
	ctx := context.Background()
	if tenant != "" {
		ctx = core.WithTenant(ctx, core.TenantID(tenant))
	}
	if run != "" {
		ctx = telemetry.WithRunID(ctx, run)
	}
	return ctx
}

func (w *telemetryWorld) currentCtx() context.Context {
	return w.ctxFor(w.tenant, w.run)
}

func (w *telemetryWorld) startSpan(ctx context.Context, name string) context.Context {
	spanCtx, span := w.setup.Tracer().Start(ctx, name)
	w.spanRefs[name] = telemetrySpan{ctx: spanCtx, span: span}
	return spanCtx
}

func (w *telemetryWorld) span(name string) (telemetrySpan, error) {
	ref, ok := w.spanRefs[name]
	if !ok {
		return telemetrySpan{}, fmt.Errorf("span %q was never started", name)
	}
	return ref, nil
}

func (w *telemetryWorld) metric(ctx context.Context, name string, value int64, labels ...attribute.KeyValue) error {
	counter, err := w.setup.Meter().Int64Counter(name)
	if err != nil {
		return err
	}
	counter.Add(ctx, value, metric.WithAttributes(labels...))
	return nil
}

func (w *telemetryWorld) markRecording() {
	w.before = w.clockNow()
}

func (w *telemetryWorld) markRecorded() {
	w.after = w.clockNow()
}

func (w *telemetryWorld) clockNow() time.Time {
	return w.setup.ClockNow()
}

func registerTelemetry(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Telemetry; w != nil {
			w.teardown()
		}
		return ctx, nil
	})

	sc.Step(`^an embedded telemetry setup with a fake clock$`, func(ctx context.Context) error {
		return world(ctx).Telemetry.startEmbedded(ctx, world(ctx).Core)
	})
	sc.Step(`^a cloud telemetry setup with a fake clock that exports to a simulated collector$`, func(ctx context.Context) error {
		return world(ctx).Telemetry.startCloud(ctx, world(ctx).Core)
	})

	sc.Step(`^tenant "([^"]*)" runs run "([^"]*)"$`, func(ctx context.Context, tenant, run string) error {
		w := world(ctx).Telemetry
		w.tenant, w.run = tenant, run
		return nil
	})
	sc.Step(`^run "([^"]*)" of tenant "([^"]*)" is running$`, func(ctx context.Context, run, tenant string) error {
		w := world(ctx).Telemetry
		w.tenant, w.run = tenant, run
		return nil
	})
	sc.Step(`^run "([^"]*)" and run "([^"]*)" of tenant "([^"]*)" are running$`, func(ctx context.Context, first, second, tenant string) error {
		w := world(ctx).Telemetry
		w.tenant, w.run = tenant, first
		_ = second
		return nil
	})
	sc.Step(`^the span store retention period is (\d+) hours?$`, func(ctx context.Context, hours int) error {
		w := world(ctx).Telemetry
		setup, err := telemetry.New(telemetry.Config{
			Profile:         telemetry.ProfileEmbedded,
			Clock:           telemetryClock{core: world(ctx).Core},
			Spans:           w.spans,
			RetentionPeriod: time.Duration(hours) * time.Hour,
		})
		if err != nil {
			return err
		}
		w.setup = setup
		return nil
	})

	sc.Step(`^(\d+) milliseconds pass on the clock$`, func(ctx context.Context, ms int) error {
		world(ctx).Core.clock.Advance(time.Duration(ms) * time.Millisecond)
		return nil
	})
	sc.Step(`^the clock advances by (\d+) seconds?$`, func(ctx context.Context, seconds int) error {
		world(ctx).Core.clock.Advance(time.Duration(seconds) * time.Second)
		return nil
	})
	sc.Step(`^(\d+) hours and (\d+) (seconds?|minutes?) pass on the clock and the span store purges expired spans$`, func(ctx context.Context, hours, amount int, unit string) error {
		w := world(ctx).Telemetry
		d := time.Duration(hours) * time.Hour
		if strings.HasPrefix(unit, "minute") {
			d += time.Duration(amount) * time.Minute
		} else {
			d += time.Duration(amount) * time.Second
		}
		world(ctx).Core.clock.Advance(d)
		_, err := w.setup.PurgeSpans(ctx)
		return err
	})

	sc.Step(`^the telemetry setup holds a credential "([^"]*)" with the value "([^"]*)"$`, func(ctx context.Context, name, value string) error {
		world(ctx).Telemetry.setup.SetCredential(value)
		return nil
	})

	sc.Step(`^a? ?span "([^"]*)" is started and ended$`, func(ctx context.Context, name string) error {
		w := world(ctx).Telemetry
		w.startSpan(w.currentCtx(), name)
		ref, _ := w.span(name)
		ref.span.End()
		return nil
	})
	sc.Step(`^span "([^"]*)" is started$`, func(ctx context.Context, name string) error {
		w := world(ctx).Telemetry
		w.startSpan(w.currentCtx(), name)
		return nil
	})
	sc.Step(`^a span "([^"]*)" is started$`, func(ctx context.Context, name string) error {
		w := world(ctx).Telemetry
		w.startSpan(w.currentCtx(), name)
		return nil
	})
	sc.Step(`^span "([^"]*)" is started with parent span "([^"]*)"$`, func(ctx context.Context, name, parent string) error {
		w := world(ctx).Telemetry
		ref, err := w.span(parent)
		if err != nil {
			return err
		}
		w.startSpan(ref.ctx, name)
		return nil
	})
	sc.Step(`^span "([^"]*)" is ended$`, func(ctx context.Context, name string) error {
		w := world(ctx).Telemetry
		ref, err := w.span(name)
		if err != nil {
			return err
		}
		ref.span.End()
		return nil
	})
	sc.Step(`^span "([^"]*)" is ended with status "([^"]*)"$`, func(ctx context.Context, name, status string) error {
		w := world(ctx).Telemetry
		ref, err := w.span(name)
		if err != nil {
			return err
		}
		ref.span.SetStatus(statusCode(status), status)
		ref.span.End()
		return nil
	})
	sc.Step(`^span "([^"]*)" is started and ended with status "([^"]*)"$`, func(ctx context.Context, name, status string) error {
		w := world(ctx).Telemetry
		w.startSpan(w.currentCtx(), name)
		ref, _ := w.span(name)
		ref.span.SetStatus(statusCode(status), status)
		ref.span.End()
		return nil
	})
	sc.Step(`^span "([^"]*)" is started and ended inside run "([^"]*)"$`, func(ctx context.Context, name, run string) error {
		w := world(ctx).Telemetry
		w.startSpan(w.ctxFor(w.tenant, run), name)
		ref, _ := w.span(name)
		ref.span.End()
		return nil
	})
	sc.Step(`^spans "([^"]*)" and "([^"]*)" are started and ended$`, func(ctx context.Context, first, second string) error {
		w := world(ctx).Telemetry
		for _, name := range []string{first, second} {
			w.startSpan(w.currentCtx(), name)
			ref, _ := w.span(name)
			ref.span.End()
		}
		return nil
	})
	sc.Step(`^span "([^"]*)" is started and ended, then the clock advances by (\d+) seconds?, then span "([^"]*)" is started and ended, then the clock advances by (\d+) seconds?, then span "([^"]*)" is started and ended$`, func(ctx context.Context, first string, firstSeconds int, second string, secondSeconds int, third string) error {
		w := world(ctx).Telemetry
		steps := []struct {
			name    string
			advance int
		}{{first, firstSeconds}, {second, secondSeconds}, {third, 0}}
		for _, item := range steps {
			w.startSpan(w.currentCtx(), item.name)
			ref, _ := w.span(item.name)
			ref.span.End()
			if item.advance > 0 {
				world(ctx).Core.clock.Advance(time.Duration(item.advance) * time.Second)
			}
		}
		return nil
	})
	sc.Step(`^span "([^"]*)" records attribute "([^"]*)" with the value (.*)$`, func(ctx context.Context, name, key, value string) error {
		ref, err := world(ctx).Telemetry.span(name)
		if err != nil {
			return err
		}
		setAttribute(ref.span, key, parseValue(value))
		return nil
	})
	sc.Step(`^the trace context of span "([^"]*)" is written to a carrier and read back$`, func(ctx context.Context, name string) error {
		w := world(ctx).Telemetry
		ref, err := w.span(name)
		if err != nil {
			return err
		}
		carrier := propagation.MapCarrier{}
		w.setup.Inject(ref.ctx, carrier)
		w.restored = w.setup.Extract(context.Background(), carrier)
		return nil
	})
	sc.Step(`^span "([^"]*)" is started from the restored trace context and ended$`, func(ctx context.Context, name string) error {
		w := world(ctx).Telemetry
		if w.restored == nil {
			return fmt.Errorf("no restored trace context: the carrier step must run first")
		}
		w.startSpan(w.restored, name)
		ref, _ := w.span(name)
		ref.span.End()
		return nil
	})

	sc.Step(`^a log record "([^"]*)" is written$`, func(ctx context.Context, message string) error {
		world(ctx).Telemetry.setup.Logger().InfoContext(world(ctx).Telemetry.currentCtx(), message)
		return nil
	})
	sc.Step(`^a log record is written inside a span "([^"]*)" of run "([^"]*)"$`, func(ctx context.Context, name, run string) error {
		w := world(ctx).Telemetry
		spanCtx := w.startSpan(w.ctxFor(w.tenant, run), name)
		w.setup.Logger().InfoContext(spanCtx, "inside")
		ref, _ := w.span(name)
		ref.span.End()
		return nil
	})
	sc.Step(`^a log record is written outside any span$`, func(ctx context.Context) error {
		world(ctx).Telemetry.setup.Logger().InfoContext(world(ctx).Telemetry.currentCtx(), "outside")
		return nil
	})
	sc.Step(`^a log record carrying "([^"]*)" is written$`, func(ctx context.Context, message string) error {
		world(ctx).Telemetry.setup.Logger().InfoContext(world(ctx).Telemetry.currentCtx(), message)
		return nil
	})
	sc.Step(`^metric "([^"]*)" is recorded with the value (-?\d+)$`, func(ctx context.Context, name string, value int64) error {
		return world(ctx).Telemetry.metric(world(ctx).Telemetry.currentCtx(), name, value)
	})
	sc.Step(`^a log record "([^"]*)" is written and metric "([^"]*)" is recorded with the value (-?\d+)$`, func(ctx context.Context, message, name string, value int64) error {
		w := world(ctx).Telemetry
		w.markRecording()
		w.setup.Logger().InfoContext(w.currentCtx(), message)
		err := w.metric(w.currentCtx(), name, value)
		w.markRecorded()
		w.lastRecord = err
		return err
	})
	sc.Step(`^metric "([^"]*)" is recorded with the value (-?\d+) and a log record "([^"]*)" is written$`, func(ctx context.Context, name string, value int64, message string) error {
		w := world(ctx).Telemetry
		w.markRecording()
		err := w.metric(w.currentCtx(), name, value)
		w.setup.Logger().InfoContext(w.currentCtx(), message)
		w.markRecorded()
		w.lastRecord = err
		return err
	})

	sc.Step(`^a span "([^"]*)" is started and ended, a log record "([^"]*)" is written, and metric "([^"]*)" is recorded with the value (-?\d+)$`, func(ctx context.Context, spanName, message, metricName string, value int64) error {
		return world(ctx).Telemetry.recordEverything(spanName, message, metricName, value)
	})
	sc.Step(`^span "([^"]*)" is started and ended, a log record "([^"]*)" is written, and metric "([^"]*)" is recorded with the value (-?\d+)$`, func(ctx context.Context, spanName, message, metricName string, value int64) error {
		return world(ctx).Telemetry.recordEverything(spanName, message, metricName, value)
	})

	sc.Step(`^the telemetry setup holds (\d+) spans? "([^"]*)" for run "([^"]*)"$`, func(ctx context.Context, count int, name, run string) error {
		n := 0
		for _, span := range world(ctx).Telemetry.setup.Spans() {
			if span.Name == name && span.RunID == run {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("held %d spans %q for run %q, want %d", n, name, run, count)
		}
		return nil
	})
	sc.Step(`^the telemetry setup holds (\d+) log records? with message "([^"]*)"$`, func(ctx context.Context, count int, message string) error {
		n := 0
		for _, rec := range world(ctx).Telemetry.setup.Logs() {
			if rec.Message == message {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("held %d log records with message %q, want %d", n, message, count)
		}
		return nil
	})
	sc.Step(`^the telemetry setup holds (\d+) metric points? named "([^"]*)" with the value (-?\d+)$`, func(ctx context.Context, count int, name string, value int64) error {
		n := 0
		for _, point := range world(ctx).Telemetry.setup.Metrics() {
			if point.Name == name && int64(point.Value) == value {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("held %d metric points %q with value %d, want %d", n, name, value, count)
		}
		return nil
	})
	sc.Step(`^span "([^"]*)" starts at "([^"]*)" and ends at "([^"]*)"$`, func(ctx context.Context, name, start, end string) error {
		span, ok := world(ctx).Telemetry.setup.SpanByName(name)
		if !ok {
			return fmt.Errorf("span %q not kept", name)
		}
		return compareTimes(span.Start, span.End, start, end)
	})
	sc.Step(`^the log record "([^"]*)" and the metric point "([^"]*)" have time "([^"]*)"$`, func(ctx context.Context, message, name, at string) error {
		want, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return err
		}
		var logTime, metricTime time.Time
		for _, rec := range world(ctx).Telemetry.setup.Logs() {
			if rec.Message == message {
				logTime = rec.Time
			}
		}
		for _, point := range world(ctx).Telemetry.setup.Metrics() {
			if point.Name == name {
				metricTime = point.Time
			}
		}
		if !logTime.Equal(want) || !metricTime.Equal(want) {
			return fmt.Errorf("log time %v metric time %v, want %v", logTime, metricTime, want)
		}
		return nil
	})
	sc.Step(`^span "([^"]*)" lasts (\d+) milliseconds?$`, func(ctx context.Context, name string, ms int) error {
		span, ok := world(ctx).Telemetry.setup.SpanByName(name)
		if !ok {
			return fmt.Errorf("span %q not kept", name)
		}
		if got := span.Duration(); got != time.Duration(ms)*time.Millisecond {
			return fmt.Errorf("span %q lasts %v, want %dms", name, got, ms)
		}
		return nil
	})
	sc.Step(`^the log record has the trace id and span id of span "([^"]*)"$`, func(ctx context.Context, name string) error {
		w := world(ctx).Telemetry
		ref, err := w.span(name)
		if err != nil {
			return err
		}
		rec, err := lastLog(w.setup)
		if err != nil {
			return err
		}
		sc := ref.span.SpanContext()
		if rec.Attrs["trace_id"] != sc.TraceID().String() || rec.Attrs["span_id"] != sc.SpanID().String() {
			return fmt.Errorf("log trace/span = %v/%v, want %s/%s", rec.Attrs["trace_id"], rec.Attrs["span_id"], sc.TraceID(), sc.SpanID())
		}
		return nil
	})
	sc.Step(`^the log record has attribute "([^"]*)" with the value of run "([^"]*)"$`, func(ctx context.Context, key, run string) error {
		rec, err := lastLog(world(ctx).Telemetry.setup)
		if err != nil {
			return err
		}
		if rec.Attrs[key] != run {
			return fmt.Errorf("log attribute %q = %v, want %q", key, rec.Attrs[key], run)
		}
		return nil
	})
	sc.Step(`^the log record has attribute "([^"]*)" with the value "([^"]*)"$`, func(ctx context.Context, key, value string) error {
		rec, err := lastLog(world(ctx).Telemetry.setup)
		if err != nil {
			return err
		}
		if rec.Attrs[key] != value {
			return fmt.Errorf("log attribute %q = %v, want %q", key, rec.Attrs[key], value)
		}
		return nil
	})
	sc.Step(`^the log record has attribute "([^"]*)" with the empty value$`, func(ctx context.Context, key string) error {
		rec, err := lastLog(world(ctx).Telemetry.setup)
		if err != nil {
			return err
		}
		value, ok := rec.Attrs[key]
		if !ok || value != "" {
			return fmt.Errorf("log attribute %q = %v (present %v), want empty", key, value, ok)
		}
		return nil
	})

	sc.Step(`^the span store holds (\d+) spans? named "([^"]*)"$`, func(ctx context.Context, count int, name string) error {
		spans, err := world(ctx).Telemetry.readSpans(world(ctx).Telemetry.tenant, world(ctx).Telemetry.run)
		if err != nil {
			return err
		}
		n := 0
		for _, span := range spans {
			if span.Name == name {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("span store holds %d spans named %q, want %d", n, name, count)
		}
		return nil
	})
	sc.Step(`^the span store holds (\d+) spans$`, func(ctx context.Context, count int) error {
		var n int
		w := world(ctx).Telemetry
		if w.store == nil {
			return fmt.Errorf("no span store in this profile")
		}
		if err := w.store.Execer().QueryRow(ctx, `SELECT COUNT(*) FROM telemetry_spans`).Scan(&n); err != nil {
			return err
		}
		if n != count {
			return fmt.Errorf("span store holds %d spans, want %d", n, count)
		}
		return nil
	})
	sc.Step(`^the kept span "([^"]*)" has the trace id of span "([^"]*)"$`, func(ctx context.Context, name, parent string) error {
		w := world(ctx).Telemetry
		kept, err := keptSpan(w.setup, name)
		if err != nil {
			return err
		}
		ref, err := w.span(parent)
		if err != nil {
			return err
		}
		if kept.TraceID != ref.span.SpanContext().TraceID().String() {
			return fmt.Errorf("span %q trace id %s, want %s", name, kept.TraceID, ref.span.SpanContext().TraceID())
		}
		return nil
	})
	sc.Step(`^the kept span "([^"]*)" has parent span id of span "([^"]*)"$`, func(ctx context.Context, name, parent string) error {
		w := world(ctx).Telemetry
		kept, err := keptSpan(w.setup, name)
		if err != nil {
			return err
		}
		ref, err := w.span(parent)
		if err != nil {
			return err
		}
		if kept.ParentSpanID != ref.span.SpanContext().SpanID().String() {
			return fmt.Errorf("span %q parent %s, want %s", name, kept.ParentSpanID, ref.span.SpanContext().SpanID())
		}
		return nil
	})
	sc.Step(`^the kept span "([^"]*)" has parent span id of span "([^"]*)", name "([^"]*)", status "([^"]*)", run id "([^"]*)" and tenant "([^"]*)"$`, func(ctx context.Context, name, parent, wantName, status, run, tenant string) error {
		w := world(ctx).Telemetry
		kept, err := keptSpan(w.setup, name)
		if err != nil {
			return err
		}
		ref, err := w.span(parent)
		if err != nil {
			return err
		}
		if kept.ParentSpanID != ref.span.SpanContext().SpanID().String() {
			return fmt.Errorf("parent = %s, want %s", kept.ParentSpanID, ref.span.SpanContext().SpanID())
		}
		if kept.Name != wantName || kept.Status != status || kept.RunID != run || kept.Tenant != tenant {
			return fmt.Errorf("kept span = %+v, want name %s status %s run %s tenant %s", kept, wantName, status, run, tenant)
		}
		return nil
	})
	sc.Step(`^the kept span "([^"]*)" starts at "([^"]*)" and ends at "([^"]*)"$`, func(ctx context.Context, name, start, end string) error {
		kept, err := keptSpan(world(ctx).Telemetry.setup, name)
		if err != nil {
			return err
		}
		return compareTimes(kept.Start, kept.End, start, end)
	})
	sc.Step(`^the kept span "([^"]*)" has attribute "([^"]*)" with the value (.*)$`, func(ctx context.Context, name, key, value string) error {
		kept, err := keptSpan(world(ctx).Telemetry.setup, name)
		if err != nil {
			return err
		}
		got, ok := attrAt(kept.Attributes, key)
		if !ok {
			return fmt.Errorf("span %q has no attribute %q", name, key)
		}
		if !valueEqual(got, parseValue(value)) {
			return fmt.Errorf("span attribute %q = %v, want %v", key, got, parseValue(value))
		}
		return nil
	})

	sc.Step(`^tenant "([^"]*)" reads the spans of run "([^"]*)"$`, func(ctx context.Context, tenant, run string) error {
		w := world(ctx).Telemetry
		spans, err := w.readSpans(tenant, run)
		w.lastRead, w.lastErr = spans, err
		return nil
	})
	sc.Step(`^tenant "([^"]*)" reads the spans named (.*) in that order$`, func(ctx context.Context, tenant, names string) error {
		w := world(ctx).Telemetry
		spans, err := w.readSpans(tenant, w.run)
		w.lastRead, w.lastErr = spans, err
		return compareSpanNames(spans, err, parseQuotedList(names))
	})
	sc.Step(`^tenant "([^"]*)" reads the spans in span id order$`, func(ctx context.Context, tenant string) error {
		w := world(ctx).Telemetry
		spans, err := w.readSpans(tenant, w.run)
		w.lastRead, w.lastErr = spans, err
		if err != nil {
			return err
		}
		for i := 1; i < len(spans); i++ {
			if spans[i-1].SpanID > spans[i].SpanID {
				return fmt.Errorf("spans are not in span id order: %s before %s", spans[i-1].SpanID, spans[i].SpanID)
			}
		}
		return nil
	})
	sc.Step(`^tenant "([^"]*)" reads (\d+) spans? named (.*) in that order$`, func(ctx context.Context, tenant string, count int, names string) error {
		w := world(ctx).Telemetry
		spans, err := w.readSpans(tenant, w.run)
		w.lastRead, w.lastErr = spans, err
		want := parseQuotedList(names)
		if len(want) != count {
			return fmt.Errorf("step names %d spans but says %d", len(want), count)
		}
		return compareSpanNames(spans, err, want)
	})
	sc.Step(`^tenant "([^"]*)" reads (\d+) spans? of run "([^"]*)"$`, func(ctx context.Context, tenant string, count int, run string) error {
		w := world(ctx).Telemetry
		spans, err := w.readSpans(tenant, run)
		w.lastRead, w.lastErr = spans, err
		if err != nil {
			return err
		}
		if len(spans) != count {
			return fmt.Errorf("read %d spans, want %d", len(spans), count)
		}
		return nil
	})
	sc.Step(`^the read returns (\d+) spans? and fails with code "([^"]*)"$`, func(ctx context.Context, count int, code string) error {
		w := world(ctx).Telemetry
		if len(w.lastRead) != count {
			return fmt.Errorf("read %d spans, want %d", len(w.lastRead), count)
		}
		if w.lastErr == nil {
			return fmt.Errorf("read succeeded, want code %s", code)
		}
		if got := core.CodeOf(w.lastErr); string(got) != code {
			return fmt.Errorf("read failed with %s, want %s", got, code)
		}
		return nil
	})

	sc.Step(`^a log record, a span attribute and a metric label each carry the text "([^"]*)"$`, func(ctx context.Context, text string) error {
		w := world(ctx).Telemetry
		w.setup.Logger().InfoContext(w.currentCtx(), text)
		w.startSpan(w.currentCtx(), "redaction")
		ref, _ := w.span("redaction")
		ref.span.SetAttributes(attribute.String("text", text))
		ref.span.End()
		return w.metric(w.currentCtx(), "redaction", 1, attribute.String("text", text))
	})
	sc.Step(`^no kept log record, span attribute or metric label contains "([^"]*)"$`, func(ctx context.Context, text string) error {
		if got := world(ctx).Telemetry.contains(text); got {
			return fmt.Errorf("%q still appears in a kept signal", text)
		}
		return nil
	})
	sc.Step(`^each records "([^"]*)"$`, func(ctx context.Context, want string) error {
		w := world(ctx).Telemetry
		rec, err := lastLog(w.setup)
		if err != nil {
			return err
		}
		span, err := keptSpan(w.setup, "redaction")
		if err != nil {
			return err
		}
		point, err := lastMetric(w.setup, "redaction")
		if err != nil {
			return err
		}
		if rec.Message != want {
			return fmt.Errorf("log message = %q, want %q", rec.Message, want)
		}
		if !valueEqual(span.Attributes["text"], want) {
			return fmt.Errorf("span attribute = %v, want %q", span.Attributes["text"], want)
		}
		if !valueEqual(point.Labels["text"], want) {
			return fmt.Errorf("metric label = %v, want %q", point.Labels["text"], want)
		}
		return nil
	})
	sc.Step(`^a log record, a span attribute and a metric label carry the value "([^"]*)" under the key path "([^"]*)"$`, func(ctx context.Context, value, path string) error {
		w := world(ctx).Telemetry
		w.setup.Logger().InfoContext(w.currentCtx(), "redaction", slog.String(path, value))
		w.startSpan(w.currentCtx(), "redaction")
		ref, _ := w.span("redaction")
		ref.span.SetAttributes(attribute.String(path, value))
		ref.span.End()
		return w.metric(w.currentCtx(), "redaction", 1, attribute.String(path, value))
	})
	sc.Step(`^the log record, the span attribute and the metric label record "(.*)" at "(.*)"$`, func(ctx context.Context, want, path string) error {
		w := world(ctx).Telemetry
		rec, err := lastLog(w.setup)
		if err != nil {
			return err
		}
		span, err := keptSpan(w.setup, "redaction")
		if err != nil {
			return err
		}
		point, err := lastMetric(w.setup, "redaction")
		if err != nil {
			return err
		}
		for _, signal := range []struct {
			name  string
			attrs map[string]any
		}{{"log record", rec.Attrs}, {"span attribute", span.Attributes}, {"metric label", point.Labels}} {
			got, ok := attrAt(signal.attrs, path)
			if !ok {
				return fmt.Errorf("%s has no value at %q", signal.name, path)
			}
			if !valueEqual(got, want) {
				return fmt.Errorf("%s at %q = %v, want %q", signal.name, path, got, want)
			}
		}
		return nil
	})
	sc.Step(`^a log record, a span attribute and a metric label carry (\d+) at "([^"]*)" and "([^"]*)" at "([^"]*)"$`, func(ctx context.Context, number int, numberPath, text, textPath string) error {
		w := world(ctx).Telemetry
		w.setup.Logger().InfoContext(w.currentCtx(), "redaction", slog.Int64(numberPath, int64(number)), slog.String(textPath, text))
		w.startSpan(w.currentCtx(), "redaction")
		ref, _ := w.span("redaction")
		ref.span.SetAttributes(attribute.Int(numberPath, number), attribute.String(textPath, text))
		ref.span.End()
		return w.metric(w.currentCtx(), "redaction", 1, attribute.Int(numberPath, number), attribute.String(textPath, text))
	})
	sc.Step(`^the log record, the span attribute and the metric label record (\d+) at "([^"]*)" and "([^"]*)" at "([^"]*)"$`, func(ctx context.Context, number int, numberPath, text, textPath string) error {
		w := world(ctx).Telemetry
		rec, err := lastLog(w.setup)
		if err != nil {
			return err
		}
		span, err := keptSpan(w.setup, "redaction")
		if err != nil {
			return err
		}
		point, err := lastMetric(w.setup, "redaction")
		if err != nil {
			return err
		}
		for _, signal := range []struct {
			name  string
			attrs map[string]any
		}{{"log record", rec.Attrs}, {"span attribute", span.Attributes}, {"metric label", point.Labels}} {
			gotNumber, ok := attrAt(signal.attrs, numberPath)
			if !ok || !valueEqual(gotNumber, int64(number)) {
				return fmt.Errorf("%s at %q = %v, want %d", signal.name, numberPath, gotNumber, number)
			}
			gotText, ok := attrAt(signal.attrs, textPath)
			if !ok || !valueEqual(gotText, text) {
				return fmt.Errorf("%s at %q = %v, want %q", signal.name, textPath, gotText, text)
			}
		}
		return nil
	})

	sc.Step(`^the telemetry setup is flushed$`, func(ctx context.Context) error {
		return world(ctx).Telemetry.setup.Flush(ctx)
	})
	sc.Step(`^the telemetry setup makes (\d+) export attempts?$`, func(ctx context.Context, count int) error {
		w := world(ctx).Telemetry
		for i := 0; i < count; i++ {
			w.lastErr = w.setup.ExportOnce(ctx)
		}
		return nil
	})
	sc.Step(`^the simulated collector answers after (\d+) seconds? of clock time$`, func(ctx context.Context, seconds int) error {
		world(ctx).Telemetry.collector.SetSlow(time.Duration(seconds) * time.Second)
		return nil
	})
	sc.Step(`^the simulated collector is unreachable$`, func(ctx context.Context) error {
		world(ctx).Telemetry.collector.SetUnreachable(true)
		return nil
	})
	sc.Step(`^the simulated collector becomes reachable$`, func(ctx context.Context) error {
		world(ctx).Telemetry.collector.SetUnreachable(false)
		return nil
	})
	sc.Step(`^recording returns without the clock advancing$`, func(ctx context.Context) error {
		w := world(ctx).Telemetry
		if !w.before.Equal(w.after) {
			return fmt.Errorf("recording advanced the clock from %v to %v", w.before, w.after)
		}
		return nil
	})
	sc.Step(`^the collector has received (\d+) spans?$`, func(ctx context.Context, count int) error {
		if got := len(world(ctx).Telemetry.collector.Spans()); got != count {
			return fmt.Errorf("collector has %d spans, want %d", got, count)
		}
		return nil
	})
	sc.Step(`^the collector received (\d+) spans? named "([^"]*)"(?: over OTLP)?$`, func(ctx context.Context, count int, name string) error {
		n := 0
		for _, span := range world(ctx).Telemetry.collector.Spans() {
			if span.Name == name {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("collector received %d spans named %q, want %d", n, name, count)
		}
		return nil
	})
	sc.Step(`^the collector received (\d+) log records? with message "([^"]*)"(?: over OTLP)?$`, func(ctx context.Context, count int, message string) error {
		n := 0
		for _, rec := range world(ctx).Telemetry.collector.Logs() {
			if rec.Message == message {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("collector received %d log records with message %q, want %d", n, message, count)
		}
		return nil
	})
	sc.Step(`^the collector received (\d+) metric points? named "([^"]*)" with the value (-?\d+)(?: over OTLP)?$`, func(ctx context.Context, count int, name string, value int64) error {
		n := 0
		for _, point := range world(ctx).Telemetry.collector.Metrics() {
			if point.Name == name && int64(point.Value) == value {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("collector received %d metric points %q with value %d, want %d", n, name, value, count)
		}
		return nil
	})
	sc.Step(`^the collector received (\d+) metric points? named "([^"]*)"$`, func(ctx context.Context, count int, name string) error {
		n := 0
		for _, point := range world(ctx).Telemetry.collector.Metrics() {
			if point.Name == name {
				n++
			}
		}
		if n != count {
			return fmt.Errorf("collector received %d metric points %q, want %d", n, name, count)
		}
		return nil
	})
	sc.Step(`^the collector received (\d+) log records? with message "([^"]*)" and (\d+) metric points? named "([^"]*)"$`, func(ctx context.Context, logCount int, message string, metricCount int, name string) error {
		collector := world(ctx).Telemetry.collector
		logs := 0
		for _, rec := range collector.Logs() {
			if rec.Message == message {
				logs++
			}
		}
		metrics := 0
		for _, point := range collector.Metrics() {
			if point.Name == name {
				metrics++
			}
		}
		if logs != logCount || metrics != metricCount {
			return fmt.Errorf("collector received %d logs and %d metrics, want %d and %d", logs, metrics, logCount, metricCount)
		}
		return nil
	})
	sc.Step(`^the collector received (\d+) spans?, (\d+) log records? and (\d+) metric points?$`, func(ctx context.Context, spans, logs, metrics int) error {
		collector := world(ctx).Telemetry.collector
		gotSpans, gotLogs, gotMetrics := len(collector.Spans()), len(collector.Logs()), len(collector.Metrics())
		if gotSpans != spans || gotLogs != logs || gotMetrics != metrics {
			return fmt.Errorf("collector received %d spans, %d logs, %d metrics; want %d, %d, %d", gotSpans, gotLogs, gotMetrics, spans, logs, metrics)
		}
		return nil
	})
	sc.Step(`^no export batch the collector received contains "([^"]*)"$`, func(ctx context.Context, text string) error {
		if world(ctx).Telemetry.collector.Contains(text) {
			return fmt.Errorf("an export batch contains %q", text)
		}
		return nil
	})
	sc.Step(`^span "([^"]*)" ends with status "([^"]*)"$`, func(ctx context.Context, name, status string) error {
		kept, err := keptSpan(world(ctx).Telemetry.setup, name)
		if err != nil {
			return err
		}
		if kept.Status != status {
			return fmt.Errorf("span %q status = %s, want %s", name, kept.Status, status)
		}
		return nil
	})
	sc.Step(`^recording the log record and the metric point returns no error$`, func(ctx context.Context) error {
		w := world(ctx).Telemetry
		if w.lastRecord != nil {
			return fmt.Errorf("recording failed: %w", w.lastRecord)
		}
		return nil
	})
	sc.Step(`^the telemetry setup shuts down$`, func(ctx context.Context) error {
		w := world(ctx).Telemetry
		err := w.setup.Shutdown(ctx)
		w.shutdownOK = w.collector.Confirmed()
		w.lastErr = err
		return err
	})
	sc.Step(`^the telemetry setup is shut down$`, func(ctx context.Context) error {
		w := world(ctx).Telemetry
		err := w.setup.Shutdown(ctx)
		w.shutdownOK = w.collector.Confirmed()
		w.lastErr = err
		return err
	})
	sc.Step(`^the telemetry setup has shut down$`, func(ctx context.Context) error {
		w := world(ctx).Telemetry
		return w.setup.Shutdown(ctx)
	})
	sc.Step(`^the telemetry setup returned only after the collector confirmed it$`, func(ctx context.Context) error {
		w := world(ctx).Telemetry
		if w.shutdownOK == 0 {
			return fmt.Errorf("shutdown returned without a collector confirmation")
		}
		return nil
	})
	sc.Step(`^the telemetry setup holds (\d+) log records and (\d+) metric points$`, func(ctx context.Context, logs, metrics int) error {
		w := world(ctx).Telemetry
		if got := len(w.setup.Logs()); got != logs {
			return fmt.Errorf("held %d log records, want %d", got, logs)
		}
		if got := len(w.setup.Metrics()); got != metrics {
			return fmt.Errorf("held %d metric points, want %d", got, metrics)
		}
		return nil
	})
}

func (w *telemetryWorld) recordEverything(spanName, message, metricName string, value int64) error {
	w.markRecording()
	w.startSpan(w.currentCtx(), spanName)
	ref, _ := w.span(spanName)
	ref.span.End()
	w.setup.Logger().InfoContext(w.currentCtx(), message)
	err := w.metric(w.currentCtx(), metricName, value)
	w.markRecorded()
	w.lastRecord = err
	return err
}

func (w *telemetryWorld) readSpans(tenant, run string) ([]telemetry.SpanData, error) {
	if w.spans == nil {
		return nil, fmt.Errorf("no span store in this profile")
	}
	return w.spans.ByRun(w.ctxFor(tenant, run), run)
}

func (w *telemetryWorld) contains(text string) bool {
	for _, rec := range w.setup.Logs() {
		if strings.Contains(rec.Message, text) || containsAny(rec.Attrs, text) {
			return true
		}
	}
	for _, span := range w.setup.Spans() {
		if containsAny(span.Attributes, text) {
			return true
		}
	}
	for _, point := range w.setup.Metrics() {
		if containsAny(point.Labels, text) {
			return true
		}
	}
	return false
}

func containsAny(value any, text string) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, text)
	case map[string]any:
		for _, item := range typed {
			if containsAny(item, text) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if containsAny(item, text) {
				return true
			}
		}
	}
	return false
}

func lastLog(setup *telemetry.Setup) (telemetry.LogRecord, error) {
	logs := setup.Logs()
	if len(logs) == 0 {
		return telemetry.LogRecord{}, fmt.Errorf("no log record was kept")
	}
	return logs[len(logs)-1], nil
}

func lastMetric(setup *telemetry.Setup, name string) (telemetry.MetricPoint, error) {
	points := setup.Metrics()
	for i := len(points) - 1; i >= 0; i-- {
		if points[i].Name == name {
			return points[i], nil
		}
	}
	return telemetry.MetricPoint{}, fmt.Errorf("no metric point named %q", name)
}

func keptSpan(setup *telemetry.Setup, name string) (telemetry.SpanData, error) {
	span, ok := setup.SpanByName(name)
	if !ok {
		return telemetry.SpanData{}, fmt.Errorf("span %q not kept", name)
	}
	return span, nil
}

func compareSpanNames(spans []telemetry.SpanData, err error, want []string) error {
	if err != nil {
		return err
	}
	if len(spans) != len(want) {
		return fmt.Errorf("read %d spans, want %d", len(spans), len(want))
	}
	for i := range want {
		if spans[i].Name != want[i] {
			return fmt.Errorf("span %d = %q, want %q", i, spans[i].Name, want[i])
		}
	}
	return nil
}

func compareTimes(start, end time.Time, wantStart, wantEnd string) error {
	startAt, err := time.Parse(time.RFC3339Nano, wantStart)
	if err != nil {
		return err
	}
	endAt, err := time.Parse(time.RFC3339Nano, wantEnd)
	if err != nil {
		return err
	}
	if !start.Equal(startAt) || !end.Equal(endAt) {
		return fmt.Errorf("span start/end = %v/%v, want %v/%v", start, end, startAt, endAt)
	}
	return nil
}

func statusCode(status string) codes.Code {
	if status == "ok" {
		return codes.Ok
	}
	return codes.Error
}

func setAttribute(span trace.Span, key string, value any) {
	switch typed := value.(type) {
	case int64:
		span.SetAttributes(attribute.Int64(key, typed))
	case int:
		span.SetAttributes(attribute.Int(key, typed))
	default:
		span.SetAttributes(attribute.String(key, fmt.Sprint(value)))
	}
}

func parseValue(raw string) any {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		return raw[1 : len(raw)-1]
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return n
	}
	return raw
}

func valueEqual(got, want any) bool {
	if wantNumber, ok := toInt64(want); ok {
		gotNumber, ok := toInt64(got)
		return ok && gotNumber == wantNumber
	}
	return fmt.Sprint(got) == fmt.Sprint(want)
}

func toInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case uint64:
		return int64(typed), true
	case float64:
		return int64(typed), true
	default:
		return 0, false
	}
}

func attrAt(attrs map[string]any, path string) (any, bool) {
	if value, ok := attrs[path]; ok {
		return value, true
	}
	current := any(attrs)
	for _, part := range strings.Split(path, ".") {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func parseQuotedList(text string) []string {
	var out []string
	for {
		start := strings.Index(text, `"`)
		if start < 0 {
			return out
		}
		text = text[start+1:]
		end := strings.Index(text, `"`)
		if end < 0 {
			return out
		}
		out = append(out, text[:end])
		text = text[end+1:]
	}
}
