package steps

import (
	"context"
	"fmt"
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
	spans, err := telemetry.NewSQLSpanStore(ctx, store, 0)
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
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		world(ctx).Telemetry = newTelemetryWorld()
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Telemetry; w != nil {
			w.teardown()
		}
		return ctx, nil
	})

	registerTelemetrySetup(sc)
	registerTelemetrySpans(sc)
	registerTelemetryRedaction(sc)
	registerTelemetryExport(sc)
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

func parseQuotedNames(text string) []string {
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
