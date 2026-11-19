package telemetry

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// LogRecord is one kept log record. Attrs holds the redacted, structured
// attributes including run_id, tenant, trace_id and span_id.
type LogRecord struct {
	Time    time.Time
	Level   slog.Level
	Message string
	Attrs   map[string]any
}

// attrEntry is a handler attribute remembered with the group prefix it was
// added under, so WithGroup never moves attributes added before it.
type attrEntry struct {
	groups []string
	attr   slog.Attr
}

// logHandler records to the setup, attaches correlation ids and redacts before
// keeping or exporting (REQ-TELEM-005 through REQ-TELEM-007).
type logHandler struct {
	setup *Setup
	base  []attrEntry
	group []string
}

func (h *logHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *logHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	base := make([]attrEntry, len(h.base), len(h.base)+len(attrs))
	copy(base, h.base)
	next := &logHandler{setup: h.setup, base: base, group: append([]string(nil), h.group...)}
	for _, attr := range attrs {
		next.base = append(next.base, attrEntry{groups: append([]string(nil), h.group...), attr: attr})
	}
	return next
}

func (h *logHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	base := make([]attrEntry, len(h.base))
	copy(base, h.base)
	return &logHandler{setup: h.setup, base: base, group: append(append([]string(nil), h.group...), name)}
}

func (h *logHandler) Handle(ctx context.Context, rec slog.Record) error {
	if !h.setup.beginRecording() {
		return nil
	}
	defer h.setup.endRecording()
	attrs := map[string]any{}
	for _, entry := range h.base {
		addLogAttr(attrs, entry.groups, entry.attr)
	}
	rec.Attrs(func(attr slog.Attr) bool {
		addLogAttr(attrs, h.group, attr)
		return true
	})

	spanCtx := trace.SpanContextFromContext(ctx)
	traceID, spanID := "", ""
	if spanCtx.IsValid() {
		traceID = spanCtx.TraceID().String()
		spanID = spanCtx.SpanID().String()
	}
	attrs["run_id"] = runIDOrEmpty(ctx)
	attrs["tenant"] = tenantOrEmpty(ctx)
	attrs["trace_id"] = traceID
	attrs["span_id"] = spanID

	kept := LogRecord{
		Time:    h.setup.clock.Now(),
		Level:   rec.Level,
		Message: h.setup.redact.String(rec.Message),
		Attrs:   h.setup.redact.Map(attrs),
	}
	h.setup.appendLog(kept)
	return nil
}

func addLogAttr(attrs map[string]any, groups []string, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	current := attrs
	for _, group := range groups {
		current = childMap(current, group)
	}
	if attr.Value.Kind() == slog.KindGroup {
		target := current
		if attr.Key != "" {
			target = childMap(current, attr.Key)
		}
		for _, nested := range attr.Value.Group() {
			addLogAttr(target, nil, nested)
		}
		return
	}
	current[attr.Key] = slogValue(attr.Value)
}

func childMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	next := map[string]any{}
	parent[key] = next
	return next
}

func slogValue(value slog.Value) any {
	switch value.Kind() {
	case slog.KindString:
		return value.String()
	case slog.KindInt64:
		return value.Int64()
	case slog.KindUint64:
		return value.Uint64()
	case slog.KindFloat64:
		return value.Float64()
	case slog.KindBool:
		return value.Bool()
	case slog.KindTime:
		return value.Time()
	case slog.KindDuration:
		return value.Duration()
	case slog.KindGroup:
		group := map[string]any{}
		for _, attr := range value.Group() {
			addLogAttr(group, nil, attr)
		}
		return group
	default:
		return value.Any()
	}
}
