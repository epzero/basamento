package telemetry

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

// exporter is a bounded, non-blocking OTLP/HTTP queue. Recording only
// appends; delivery happens on Flush, Shutdown or an explicit ExportOnce, so a
// slow or unreachable collector never delays the work (REQ-TELEM-016). A batch
// that cannot be delivered is removed and not retried (REQ-TELEM-017).
type exporter struct {
	endpoint string
	client   *http.Client

	mu      sync.Mutex
	spans   []SpanData
	logs    []LogRecord
	metrics []MetricPoint
}

func newExporter(endpoint string) *exporter {
	return &exporter{
		endpoint: strings.TrimRight(endpoint, "/"),
		client:   &http.Client{Timeout: 5 * time.Second},
	}
}

func (e *exporter) enqueueSpan(span SpanData) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.spans) < maxPendingSignals {
		e.spans = append(e.spans, span)
	}
}

func (e *exporter) enqueueLog(rec LogRecord) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.logs) < maxPendingSignals {
		e.logs = append(e.logs, rec)
	}
}

func (e *exporter) enqueueMetric(point MetricPoint) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.metrics) < maxPendingSignals {
		e.metrics = append(e.metrics, point)
	}
}

func (e *exporter) flush(ctx context.Context) error {
	return e.deliver(ctx)
}

func (e *exporter) attempt(ctx context.Context) error {
	return e.deliver(ctx)
}

// deliver drains the queues and makes at most one request per signal type. A
// failed request drops that batch and is not retried.
func (e *exporter) deliver(ctx context.Context) error {
	e.mu.Lock()
	spans, logs, metrics := e.spans, e.logs, e.metrics
	e.spans, e.logs, e.metrics = nil, nil, nil
	e.mu.Unlock()

	var errs []error
	if len(spans) > 0 {
		body, err := marshalRequest(traceResourceSpans(spans))
		if err != nil {
			errs = append(errs, err)
		} else if err := e.post(ctx, "/v1/traces", body); err != nil {
			errs = append(errs, err)
		}
	}
	if len(logs) > 0 {
		body, err := marshalRequest(logsResourceLogs(logs))
		if err != nil {
			errs = append(errs, err)
		} else if err := e.post(ctx, "/v1/logs", body); err != nil {
			errs = append(errs, err)
		}
	}
	if len(metrics) > 0 {
		body, err := marshalRequest(metricsResourceMetrics(metrics))
		if err != nil {
			errs = append(errs, err)
		} else if err := e.post(ctx, "/v1/metrics", body); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// marshalRequest encodes a single ResourceSpans, ResourceLogs or
// ResourceMetrics as field 1 of the OTLP export request, so the package needs
// only the message types and not the gRPC service stubs.
func marshalRequest(message proto.Message) ([]byte, error) {
	raw, err := proto.Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("telemetry: marshal OTLP request: %w", err)
	}
	body := protowire.AppendTag(nil, 1, protowire.BytesType)
	return protowire.AppendBytes(body, raw), nil
}

func (e *exporter) post(ctx context.Context, path string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telemetry: build OTLP request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("telemetry: OTLP export: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if _, _ = io.Copy(io.Discard, resp.Body); resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telemetry: OTLP export: collector answered %d", resp.StatusCode)
	}
	return nil
}

func traceResourceSpans(spans []SpanData) *tracev1.ResourceSpans {
	out := make([]*tracev1.Span, 0, len(spans))
	for _, span := range spans {
		var code tracev1.Status_StatusCode
		switch span.Status {
		case StatusOK:
			code = tracev1.Status_STATUS_CODE_OK
		case StatusError:
			code = tracev1.Status_STATUS_CODE_ERROR
		default:
			code = tracev1.Status_STATUS_CODE_UNSET
		}
		out = append(out, &tracev1.Span{
			TraceId:           decodeID(span.TraceID),
			SpanId:            decodeID(span.SpanID),
			ParentSpanId:      decodeID(span.ParentSpanID),
			Name:              span.Name,
			StartTimeUnixNano: unixNano(span.Start),
			EndTimeUnixNano:   unixNano(span.End),
			Status:            &tracev1.Status{Code: code},
			Attributes:        keyValues(span.Attributes),
		})
	}
	return &tracev1.ResourceSpans{
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: out}},
	}
}

func logsResourceLogs(records []LogRecord) *logsv1.ResourceLogs {
	out := make([]*logsv1.LogRecord, 0, len(records))
	for _, rec := range records {
		out = append(out, &logsv1.LogRecord{
			TimeUnixNano:   unixNano(rec.Time),
			SeverityNumber: severityNumber(rec.Level),
			SeverityText:   rec.Level.String(),
			Body:           &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: rec.Message}},
			Attributes:     keyValues(rec.Attrs),
		})
	}
	return &logsv1.ResourceLogs{
		ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: out}},
	}
}

func metricsResourceMetrics(points []MetricPoint) *metricsv1.ResourceMetrics {
	out := make([]*metricsv1.Metric, 0, len(points))
	for _, point := range points {
		attrs := keyValues(point.Labels)
		metric := &metricsv1.Metric{Name: point.Name}
		switch point.Kind {
		case KindGauge:
			metric.Data = &metricsv1.Metric_Gauge{Gauge: &metricsv1.Gauge{
				DataPoints: []*metricsv1.NumberDataPoint{numberPoint(point, attrs)},
			}}
		case KindHistogram:
			sum := point.Value
			metric.Data = &metricsv1.Metric_Histogram{Histogram: &metricsv1.Histogram{
				AggregationTemporality: metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				DataPoints: []*metricsv1.HistogramDataPoint{{
					Attributes:   attrs,
					TimeUnixNano: unixNano(point.Time),
					Count:        1,
					Sum:          &sum,
				}},
			}}
		default:
			metric.Data = &metricsv1.Metric_Sum{Sum: &metricsv1.Sum{
				AggregationTemporality: metricsv1.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
				IsMonotonic:            point.Kind == KindCounter,
				DataPoints:             []*metricsv1.NumberDataPoint{numberPoint(point, attrs)},
			}}
		}
		out = append(out, metric)
	}
	return &metricsv1.ResourceMetrics{
		ScopeMetrics: []*metricsv1.ScopeMetrics{{Metrics: out}},
	}
}

func numberPoint(point MetricPoint, attrs []*commonv1.KeyValue) *metricsv1.NumberDataPoint {
	dp := &metricsv1.NumberDataPoint{
		Attributes:   attrs,
		TimeUnixNano: unixNano(point.Time),
	}
	if point.IsInt {
		dp.Value = &metricsv1.NumberDataPoint_AsInt{AsInt: int64(point.Value)}
	} else {
		dp.Value = &metricsv1.NumberDataPoint_AsDouble{AsDouble: point.Value}
	}
	return dp
}

func severityNumber(level slog.Level) logsv1.SeverityNumber {
	switch level.String() {
	case "DEBUG":
		return logsv1.SeverityNumber_SEVERITY_NUMBER_DEBUG
	case "INFO":
		return logsv1.SeverityNumber_SEVERITY_NUMBER_INFO
	case "WARN":
		return logsv1.SeverityNumber_SEVERITY_NUMBER_WARN
	case "ERROR":
		return logsv1.SeverityNumber_SEVERITY_NUMBER_ERROR
	default:
		return logsv1.SeverityNumber_SEVERITY_NUMBER_UNSPECIFIED
	}
}

func keyValues(attrs map[string]any) []*commonv1.KeyValue {
	if len(attrs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(attrs))
	for key := range attrs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]*commonv1.KeyValue, 0, len(keys))
	for _, key := range keys {
		out = append(out, &commonv1.KeyValue{Key: key, Value: anyValue(attrs[key])})
	}
	return out
}

// anyValue encodes a value the redactor returned. Every shape it does not know
// is redacted instead of formatted: an exporter must never turn an arbitrary Go
// value into text that skipped redaction (REQ-TELEM-006, REQ-TELEM-007).
func anyValue(value any) *commonv1.AnyValue {
	switch typed := value.(type) {
	case nil:
		return nil
	case string:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: typed}}
	case bool:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_BoolValue{BoolValue: typed}}
	case int:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: int64(typed)}}
	case int32:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: int64(typed)}}
	case int64:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: typed}}
	case uint:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: int64(typed)}}
	case uint32:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: int64(typed)}}
	case uint64:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: int64(typed)}}
	case float32:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_DoubleValue{DoubleValue: float64(typed)}}
	case float64:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_DoubleValue{DoubleValue: typed}}
	case []any:
		values := make([]*commonv1.AnyValue, 0, len(typed))
		for _, item := range typed {
			values = append(values, anyValue(item))
		}
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_ArrayValue{ArrayValue: &commonv1.ArrayValue{Values: values}}}
	case map[string]any:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_KvlistValue{KvlistValue: &commonv1.KeyValueList{Values: keyValues(typed)}}}
	default:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: Redacted}}
	}
}

func decodeID(id string) []byte {
	raw, err := hex.DecodeString(id)
	if err != nil {
		return nil
	}
	return raw
}

func unixNano(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.UnixNano())
}
