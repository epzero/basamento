// Package telemetry is documented in doc.go.
package telemetry

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/epzero/basamento/core"
)

// Profiles accepted by Config.Profile.
const (
	// ProfileEmbedded keeps ended spans in the configured SpanStore.
	ProfileEmbedded = "embedded"
	// ProfileCloud exports every signal over OTLP to Config.CollectorURL.
	ProfileCloud = "cloud"

	// DefaultServiceName names the tracer and meter when Config.ServiceName is empty.
	DefaultServiceName = "basamento"
)

// DefaultRetentionPeriod is the span retention used when Config.RetentionPeriod
// is zero in the embedded profile (REQ-TELEM-014).
const DefaultRetentionPeriod = 24 * time.Hour

// maxKeptSignals bounds the in-process copies kept for inspection.
const maxKeptSignals = 1024

// Config configures a telemetry Setup.
type Config struct {
	// Profile is ProfileEmbedded or ProfileCloud.
	Profile string
	// CollectorURL is the OTLP/HTTP endpoint used in the cloud profile.
	CollectorURL string
	// Clock is the only source of time for records, spans and metric points.
	Clock core.Clock
	// RetentionPeriod is how long an embedded span stays readable. Zero means
	// DefaultRetentionPeriod; the span store applies it, and a negative value is
	// refused when the store is built.
	RetentionPeriod time.Duration
	// Secrets supplies extra credential values to redact. Values added through
	// SetCredential are always redacted too; a nil source simply adds none.
	Secrets SecretsSource
	// Spans keeps ended spans in the embedded profile. It is required there and
	// ignored in the cloud profile.
	Spans SpanStore
	// ServiceName names the tracer and meter. Empty means DefaultServiceName.
	ServiceName string
}

// Setup is one wired telemetry stack. Its methods are safe for concurrent use.
type Setup struct {
	cfg     Config
	clock   core.Clock
	redact  redactor
	secrets *StaticSecrets
	export  *exporter

	logger *slog.Logger
	tracer trace.Tracer
	meter  metric.Meter
	tp     *tracerProvider
	mp     *meterProvider
	prop   propagation.TextMapPropagator

	mu          sync.Mutex
	closed      bool
	logs        []LogRecord
	metrics     []MetricPoint
	spans       []SpanData
	droppedSpan atomic.Int64
}

// New validates cfg and returns a ready setup. The embedded profile requires a
// SpanStore; the cloud profile requires a CollectorURL.
func New(cfg Config) (*Setup, error) {
	if cfg.Profile != ProfileEmbedded && cfg.Profile != ProfileCloud {
		return nil, core.NewError(core.CodeInvalidArgument, "telemetry: unknown profile", nil)
	}
	if cfg.Clock == nil {
		cfg.Clock = core.SystemClock{}
	}
	if cfg.RetentionPeriod < 0 {
		return nil, core.NewError(core.CodeInvalidArgument, "telemetry: negative retention period", nil)
	}
	if cfg.ServiceName == "" {
		cfg.ServiceName = DefaultServiceName
	}
	if cfg.Profile == ProfileEmbedded && cfg.Spans == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "telemetry: embedded profile requires a span store", nil)
	}
	if cfg.Profile == ProfileCloud && cfg.CollectorURL == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "telemetry: cloud profile requires a collector URL", nil)
	}

	own := NewStaticSecrets()
	sources := []SecretsSource{own}
	if cfg.Secrets != nil {
		sources = append(sources, cfg.Secrets)
	}
	s := &Setup{
		cfg:     cfg,
		clock:   cfg.Clock,
		redact:  redactor{sources: sources},
		secrets: own,
		prop:    propagation.TraceContext{},
	}
	s.logger = slog.New(&logHandler{setup: s})
	s.tp = &tracerProvider{setup: s}
	s.tracer = s.tp.Tracer(cfg.ServiceName)
	s.mp = &meterProvider{setup: s}
	s.meter = s.mp.Meter(cfg.ServiceName)
	if cfg.Profile == ProfileCloud {
		s.export = newExporter(cfg.CollectorURL, cfg.ServiceName)
	}
	return s, nil
}

// ClockNow returns the setup's current time. It exposes no wall clock; it is
// the value timestamps are taken from (REQ-TELEM-003).
func (s *Setup) ClockNow() time.Time { return s.clock.Now() }

// Logger returns the slog logger of the setup.
func (s *Setup) Logger() *slog.Logger { return s.logger }

// Tracer returns the OpenTelemetry tracer of the setup.
func (s *Setup) Tracer() trace.Tracer { return s.tracer }

// Meter returns the OpenTelemetry meter of the setup.
func (s *Setup) Meter() metric.Meter { return s.meter }

// Inject writes the active trace context of ctx to carrier using W3C
// TraceContext (REQ-TELEM-021).
func (s *Setup) Inject(ctx context.Context, carrier propagation.TextMapCarrier) {
	s.prop.Inject(ctx, carrier)
}

// Extract restores a trace context from carrier and returns a context whose
// child spans join that trace (REQ-TELEM-021).
func (s *Setup) Extract(ctx context.Context, carrier propagation.TextMapCarrier) context.Context {
	return s.prop.Extract(ctx, carrier)
}

// SetCredential registers a credential value to redact from every signal. The
// value is matched; a name never reaches a signal and is not needed.
func (s *Setup) SetCredential(value string) {
	s.secrets.Add(value)
}

// Logs returns a copy of the log records kept in this process.
func (s *Setup) Logs() []LogRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LogRecord, len(s.logs))
	copy(out, s.logs)
	return out
}

// Metrics returns a copy of the metric points kept in this process.
func (s *Setup) Metrics() []MetricPoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MetricPoint, len(s.metrics))
	copy(out, s.metrics)
	return out
}

// Spans returns a copy of the spans ended in this process, in end order.
func (s *Setup) Spans() []SpanData {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SpanData, len(s.spans))
	copy(out, s.spans)
	return out
}

// SpanByName returns the last kept span with name.
func (s *Setup) SpanByName(name string) (SpanData, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.spans) - 1; i >= 0; i-- {
		if s.spans[i].Name == name {
			return s.spans[i], true
		}
	}
	return SpanData{}, false
}

// PurgeSpans deletes the embedded spans that ended more than the span store's
// retention period ago and reports how many rows went (REQ-TELEM-014). The
// store owns the retention and its default; the setup only supplies the time.
func (s *Setup) PurgeSpans(ctx context.Context) (int64, error) {
	if s.cfg.Profile != ProfileEmbedded || s.cfg.Spans == nil {
		return 0, nil
	}
	return s.cfg.Spans.Purge(ctx, s.clock.Now())
}

// ExportOnce makes one delivery attempt for each pending signal type. A batch
// that fails is dropped and not retried (REQ-TELEM-017). It is the deterministic
// hook the gate uses for a single export attempt.
func (s *Setup) ExportOnce(ctx context.Context) error {
	if s.export == nil {
		return nil
	}
	return s.export.attempt(ctx)
}

// Flush delivers every finished recording that is still pending (REQ-TELEM-022).
// In the embedded profile it is a no-op. An undeliverable batch is dropped; the
// returned error names the failure but never affects the recording itself.
func (s *Setup) Flush(ctx context.Context) error {
	if s.export == nil {
		return nil
	}
	return s.export.flush(ctx)
}

// Shutdown drains the finished recordings, then makes every later record call a
// no-op (REQ-TELEM-022, REQ-TELEM-023). It returns only after every signal
// accepted before the call has been delivered or dropped, including a delivery
// another goroutine already had in flight. It is idempotent.
func (s *Setup) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	if s.export == nil {
		return nil
	}
	return s.export.shutdown(ctx)
}

// DroppedSpans reports how many ended spans the embedded span store rejected.
// Recording never fails because of a store error; the count is how a caller
// notices that spans are missing.
func (s *Setup) DroppedSpans() int64 { return s.droppedSpan.Load() }

func (s *Setup) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *Setup) recordLog(rec LogRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.logs = appendCapped(s.logs, rec)
	if s.export != nil {
		// Enqueue under the same lock that Shutdown takes: once closed is set
		// no record can land in a queue after the final drain.
		s.export.enqueueLog(rec)
	}
}

func (s *Setup) recordMetric(point MetricPoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.metrics = appendCapped(s.metrics, point)
	if s.export != nil {
		s.export.enqueueMetric(point)
	}
}

// recordSpan reports whether the span was accepted. A span rejected here must
// not be written to the span store afterwards.
func (s *Setup) recordSpan(span SpanData) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.spans = appendCapped(s.spans, span)
	if s.export != nil {
		s.export.enqueueSpan(span)
	}
	return true
}

// appendCapped keeps the newest maxKeptSignals entries without growing forever.
func appendCapped[T any](items []T, item T) []T {
	if len(items) < maxKeptSignals {
		return append(items, item)
	}
	copy(items, items[1:])
	items[len(items)-1] = item
	return items
}
