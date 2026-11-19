package telemetry

import (
	"context"
	"crypto/rand"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// Span status values kept by the span store.
const (
	StatusUnset = "unset"
	StatusOK    = "ok"
	StatusError = "error"
)

// SpanData is one ended span. It is what the embedded store keeps and the run
// inspector reads (REQ-TELEM-011).
type SpanData struct {
	TraceID      string
	SpanID       string
	ParentSpanID string
	Name         string
	Start        time.Time
	End          time.Time
	Status       string
	RunID        string
	Tenant       string
	Attributes   map[string]any
}

// Duration is End minus Start.
func (s SpanData) Duration() time.Duration { return s.End.Sub(s.Start) }

// tracerProvider is the trace.TracerProvider of one Setup.
type tracerProvider struct {
	embedded.TracerProvider
	setup *Setup
}

func (p *tracerProvider) Tracer(string, ...trace.TracerOption) trace.Tracer {
	return &tracer{provider: p}
}

type tracer struct {
	embedded.Tracer
	provider *tracerProvider
}

func (t *tracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if t.provider.setup.isClosed() {
		// A shut-down setup records nothing; returning the context's own span
		// would let a caller end a span it did not start.
		return ctx, trace.SpanFromContext(context.Background())
	}
	cfg := trace.NewSpanStartConfig(opts...)
	parent := trace.SpanContextFromContext(ctx)
	if cfg.NewRoot() {
		parent = trace.SpanContext{}
	}

	span := &recordingSpan{provider: t.provider}
	span.name = name
	span.attributes = map[string]any{}
	if parent.IsValid() {
		span.traceID = parent.TraceID()
		span.parentSpanID = parent.SpanID()
	} else {
		span.traceID = newTraceID()
	}
	span.spanID = newSpanID()
	span.start = t.provider.setup.clock.Now()
	span.runID = runIDOrEmpty(ctx)
	span.tenant = tenantOrEmpty(ctx)
	for _, kv := range cfg.Attributes() {
		span.setAttribute(kv)
	}
	return trace.ContextWithSpan(ctx, span), span
}

// recordingSpan is the trace.Span implementation. It reads time only from the
// setup clock and redacts attributes before keeping or exporting them.
type recordingSpan struct {
	embedded.Span
	provider *tracerProvider

	mu           sync.Mutex
	name         string
	traceID      trace.TraceID
	spanID       trace.SpanID
	parentSpanID trace.SpanID
	start        time.Time
	end          time.Time
	status       codes.Code
	runID        string
	tenant       string
	attributes   map[string]any
	ended        bool
}

func (s *recordingSpan) End(...trace.SpanEndOption) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	s.end = s.provider.setup.clock.Now()
	span := SpanData{
		TraceID:      s.traceID.String(),
		SpanID:       s.spanID.String(),
		ParentSpanID: s.parentSpanID.String(),
		Name:         s.name,
		Start:        s.start,
		End:          s.end,
		Status:       statusName(s.status),
		RunID:        s.runID,
		Tenant:       s.tenant,
		Attributes:   s.provider.setup.redact.Map(s.attributes),
	}
	if s.parentSpanID == (trace.SpanID{}) {
		span.ParentSpanID = ""
	}
	s.mu.Unlock()

	setup := s.provider.setup
	if setup.isClosed() {
		return
	}
	setup.recordSpan(span)
	if setup.cfg.Profile == ProfileEmbedded && setup.cfg.Spans != nil {
		_ = setup.cfg.Spans.Append(context.Background(), span)
	}
}

func (s *recordingSpan) AddEvent(string, ...trace.EventOption) {}

func (s *recordingSpan) AddLink(trace.Link) {}

func (s *recordingSpan) IsRecording() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.ended && !s.provider.setup.isClosed()
}

func (s *recordingSpan) RecordError(error, ...trace.EventOption) {}

func (s *recordingSpan) SpanContext() trace.SpanContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    s.traceID,
		SpanID:     s.spanID,
		TraceFlags: trace.FlagsSampled,
	})
}

func (s *recordingSpan) SetStatus(code codes.Code, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if code <= s.status {
		return
	}
	s.status = code
}

func (s *recordingSpan) SetName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.name = name
}

func (s *recordingSpan) SetAttributes(kv ...attribute.KeyValue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range kv {
		s.setAttributeLocked(item)
	}
}

func (s *recordingSpan) TracerProvider() trace.TracerProvider { return s.provider }

func (s *recordingSpan) setAttribute(kv attribute.KeyValue) {
	s.setAttributeLocked(kv)
}

func (s *recordingSpan) setAttributeLocked(kv attribute.KeyValue) {
	s.attributes[string(kv.Key)] = kv.Value.AsInterface()
}

func statusName(code codes.Code) string {
	switch code {
	case codes.Ok:
		return StatusOK
	case codes.Error:
		return StatusError
	default:
		return StatusUnset
	}
}

func newTraceID() trace.TraceID {
	var id trace.TraceID
	_, _ = rand.Read(id[:])
	if id[0] == 0 {
		id[0] = 1
	}
	return id
}

func newSpanID() trace.SpanID {
	var id trace.SpanID
	_, _ = rand.Read(id[:])
	if id[0] == 0 {
		id[0] = 1
	}
	return id
}
