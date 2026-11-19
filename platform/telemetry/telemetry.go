// Package telemetry is documented in doc.go.
package telemetry

import (
	"context"
	"errors"
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
	// RetentionPeriod is the embedded span store's effective purge window.
	// Zero means DefaultRetentionPeriod, and a negative value is refused by
	// New. New applies it to a SpanStore that implements RetentionApplier, so
	// the configured period wins over the value the store was built with.
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

	mu       sync.Mutex
	closed   bool
	drained  bool
	inflight int
	idle     chan struct{}
	logs     []LogRecord
	metrics  []MetricPoint
	spans    []SpanData

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
	if cfg.Profile == ProfileEmbedded {
		// The effective retention is the setup's, not the value the store was
		// built with (REQ-TELEM-014). A store that does not implement
		// RetentionApplier owns its own window.
		if store, ok := cfg.Spans.(RetentionApplier); ok {
			if err := store.SetRetention(cfg.RetentionPeriod); err != nil {
				return nil, err
			}
		}
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
		s.export = newExporter(cfg.CollectorURL, cfg.ServiceName, cfg.Clock)
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

// Logs returns a deep copy of the log records kept in this process. The
// returned Attrs share no map with the kept records.
func (s *Setup) Logs() []LogRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LogRecord, len(s.logs))
	for i, rec := range s.logs {
		rec.Attrs = copyMap(rec.Attrs)
		out[i] = rec
	}
	return out
}

// Metrics returns a deep copy of the metric points kept in this process. The
// returned Labels share no map with the kept points.
func (s *Setup) Metrics() []MetricPoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]MetricPoint, len(s.metrics))
	for i, point := range s.metrics {
		point.Labels = copyMap(point.Labels)
		out[i] = point
	}
	return out
}

// Spans returns a deep copy of the spans ended in this process, in end order.
// The returned Attributes share no map with the kept spans.
func (s *Setup) Spans() []SpanData {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SpanData, len(s.spans))
	for i, span := range s.spans {
		span.Attributes = copyMap(span.Attributes)
		out[i] = span
	}
	return out
}

// SpanByName returns a deep copy of the last kept span with name.
func (s *Setup) SpanByName(name string) (SpanData, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.spans) - 1; i >= 0; i-- {
		if s.spans[i].Name == name {
			span := s.spans[i]
			span.Attributes = copyMap(span.Attributes)
			return span, true
		}
	}
	return SpanData{}, false
}

// copyMap returns a copy of m that shares no nested map or slice with it.
func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for key, value := range m {
		out[key] = copyValue(value)
	}
	return out
}

// copyValue copies the maps, slices and byte slices the redactor produces. All
// other values it keeps are immutable scalars or time.Time.
func copyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return copyMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = copyValue(item)
		}
		return out
	case []byte:
		out := make([]byte, len(typed))
		copy(out, typed)
		return out
	default:
		return value
	}
}

// PurgeSpans deletes the embedded spans that ended more than the effective
// retention period ago and reports how many rows went (REQ-TELEM-014). New
// applied Config.RetentionPeriod (or DefaultRetentionPeriod when it was zero)
// to the store, so that is the window; the setup supplies the current time.
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

// Shutdown delivers every signal the work finished recording before it returns,
// then makes every later recording a no-op (REQ-TELEM-022, REQ-TELEM-023). It
// refuses new spans, log records and metric points as soon as it starts, waits
// for the recordings already in progress, and repeats the drain until nothing
// is in flight and no signal is pending. An ended span is therefore delivered
// even when its End call lands during the drain, because the span was started
// before the call. The caller's context bounds every wait and every request: on
// expiry Shutdown stops waiting, refuses later recordings and returns ctx.Err()
// (an undelivered backlog is dropped, as REQ-TELEM-017 allows). It is
// idempotent and safe to call from several goroutines.
func (s *Setup) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	if s.export != nil {
		if err := s.export.halt(ctx); err != nil {
			s.finishRecording()
			return err
		}
	}
	for {
		if err := s.waitIdle(ctx); err != nil {
			s.finishRecording()
			return err
		}
		var drainErr error
		if s.export != nil {
			drainErr = s.export.drain(ctx)
		}
		s.mu.Lock()
		quiet := s.inflight == 0 && (s.export == nil || s.export.pending() == 0)
		if quiet {
			s.drained = true
		}
		s.mu.Unlock()
		if quiet {
			return drainErr
		}
		if err := ctx.Err(); err != nil {
			if s.export != nil {
				return errors.Join(drainErr, err)
			}
			return err
		}
	}
}

// DroppedSpans reports how many ended spans the embedded span store rejected.
// Recording never fails because of a store error; the count is how a caller
// notices that spans are missing.
func (s *Setup) DroppedSpans() int64 { return s.droppedSpan.Load() }

// DroppedExports reports how many signals the export queues dropped because a
// slow or unreachable collector kept the per-type queue at its bound. The
// oldest pending signal of that type is dropped, and recording never fails
// (REQ-TELEM-016, REQ-TELEM-017).
func (s *Setup) DroppedExports() int64 {
	if s.export == nil {
		return 0
	}
	return s.export.dropped.Load()
}

func (s *Setup) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// beginRecording registers a log record or metric point that is being built.
// Shutdown refuses new ones once it starts, then waits for the registered ones
// before the final drain, so a recording that began before the call is never
// lost.
func (s *Setup) beginRecording() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.startRecordingLocked()
	return true
}

// beginEndedSpan registers the recording of a span that was started before
// Shutdown began. Unlike a new log record or metric point, an ended span is
// still accepted while Shutdown drains: the work started it while telemetry
// was live, and REQ-TELEM-022 delivers what finished recording before the call
// returns. Only a span that ends after Shutdown has returned is refused.
func (s *Setup) beginEndedSpan() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drained {
		return false
	}
	s.startRecordingLocked()
	return true
}

func (s *Setup) startRecordingLocked() {
	if s.inflight == 0 {
		s.idle = make(chan struct{})
	}
	s.inflight++
}

func (s *Setup) endRecording() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inflight--
	if s.inflight == 0 && s.idle != nil {
		close(s.idle)
		s.idle = nil
	}
}

// waitIdle returns once no recording is in flight. The channel is re-read every
// round because a new recording can begin after a previous wait returned.
func (s *Setup) waitIdle(ctx context.Context) error {
	for {
		s.mu.Lock()
		idle := s.idle
		s.mu.Unlock()
		if idle == nil {
			return nil
		}
		select {
		case <-idle:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// finishRecording sets the state that refuses a span that ends after Shutdown
// has given up on its context.
func (s *Setup) finishRecording() {
	s.mu.Lock()
	s.drained = true
	s.mu.Unlock()
}

// appendLog keeps rec in process and enqueues it for export. The caller holds a
// recording registration, so Shutdown cannot set drained until it returns.
func (s *Setup) appendLog(rec LogRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = appendCapped(s.logs, rec)
	if s.export != nil {
		s.export.enqueueLog(rec)
	}
}

func (s *Setup) appendMetric(point MetricPoint) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics = appendCapped(s.metrics, point)
	if s.export != nil {
		s.export.enqueueMetric(point)
	}
}

// acceptSpan keeps and enqueues one span the caller has already registered with
// beginEndedSpan. The store append is inside the registration too, so Shutdown
// does not return before an accepted span is stored.
func (s *Setup) acceptSpan(ctx context.Context, span SpanData) {
	s.mu.Lock()
	s.spans = appendCapped(s.spans, span)
	if s.export != nil {
		s.export.enqueueSpan(span)
	}
	s.mu.Unlock()
	if s.cfg.Profile != ProfileEmbedded || s.cfg.Spans == nil {
		return
	}
	if err := s.cfg.Spans.Append(ctx, span); err != nil {
		// The span was accepted; a store failure must not fail the work. The
		// counter is how a caller notices missing spans.
		s.droppedSpan.Add(1)
	}
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
