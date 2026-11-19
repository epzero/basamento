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
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

const (
	// exportBatchSize bounds one export request per signal type. Delivery
	// drains a larger queue in several bounded batches.
	exportBatchSize = 1024
	// exportTrigger is the number of pending signals that wakes the background
	// exporter. Delivery is size-triggered, never clock-triggered.
	exportTrigger = 512
)

// exporter is a non-blocking OTLP/HTTP queue with an owned delivery goroutine.
// Recording only appends and wakes the goroutine at exportTrigger, so a slow or
// unreachable collector never delays the work (REQ-TELEM-016). A batch that
// cannot be delivered is removed and not retried (REQ-TELEM-017).
type exporter struct {
	endpoint    string
	serviceName string
	client      *http.Client

	// deliverMu serialises every delivery attempt, so Flush, Shutdown, an
	// explicit ExportOnce and the background goroutine never overlap. A
	// shutdown that returns therefore has no delivery still in flight.
	deliverMu sync.Mutex

	mu      sync.Mutex
	spans   []SpanData
	logs    []LogRecord
	metrics []MetricPoint

	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
}

func newExporter(endpoint, serviceName string) *exporter {
	e := &exporter{
		endpoint:    strings.TrimRight(endpoint, "/"),
		serviceName: serviceName,
		client:      &http.Client{Timeout: 5 * time.Second},
		wake:        make(chan struct{}, 1),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	go e.run()
	return e
}

// run is the owned delivery goroutine. It drains when the queue reaches
// exportTrigger and exits when Shutdown closes stop.
func (e *exporter) run() {
	defer close(e.done)
	for {
		select {
		case <-e.stop:
			return
		case <-e.wake:
			_ = e.drain(context.Background())
		}
	}
}

func (e *exporter) enqueueSpan(span SpanData) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = append(e.spans, span)
	e.notifyLocked()
}

func (e *exporter) enqueueLog(rec LogRecord) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.logs = append(e.logs, rec)
	e.notifyLocked()
}

func (e *exporter) enqueueMetric(point MetricPoint) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.metrics = append(e.metrics, point)
	e.notifyLocked()
}

func (e *exporter) notifyLocked() {
	if len(e.spans)+len(e.logs)+len(e.metrics) < exportTrigger {
		return
	}
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *exporter) flush(ctx context.Context) error {
	return e.drain(ctx)
}

func (e *exporter) attempt(ctx context.Context) error {
	e.deliverMu.Lock()
	defer e.deliverMu.Unlock()
	_, err := e.deliverBatchLocked(ctx)
	return err
}

// shutdown stops the owned goroutine, waits for any delivery it has in flight,
// then drains the rest. It is safe to call more than once and from several
// goroutines: every call waits for delivery to finish.
func (e *exporter) shutdown(ctx context.Context) error {
	e.stopOnce.Do(func() { close(e.stop) })
	<-e.done
	return e.drain(ctx)
}

// drain delivers every pending signal in bounded batches. A failed batch is
// dropped; the remaining batches are still attempted (REQ-TELEM-017).
func (e *exporter) drain(ctx context.Context) error {
	e.deliverMu.Lock()
	defer e.deliverMu.Unlock()
	var errs []error
	for {
		more, err := e.deliverBatchLocked(ctx)
		if err != nil {
			errs = append(errs, err)
		}
		if !more {
			return errors.Join(errs...)
		}
	}
}

// deliverBatchLocked takes at most exportBatchSize signals per type and makes
// at most one request per type. A failed request drops that batch and is not
// retried.
func (e *exporter) deliverBatchLocked(ctx context.Context) (bool, error) {
	spans, logs, metrics := e.takeLocked(exportBatchSize)
	if len(spans) == 0 && len(logs) == 0 && len(metrics) == 0 {
		return false, nil
	}

	var errs []error
	if len(spans) > 0 {
		body, err := marshalRequest(traceResourceSpans(spans, e.serviceName))
		if err != nil {
			errs = append(errs, err)
		} else if err := e.post(ctx, "/v1/traces", body); err != nil {
			errs = append(errs, err)
		}
	}
	if len(logs) > 0 {
		body, err := marshalRequest(logsResourceLogs(logs, e.serviceName))
		if err != nil {
			errs = append(errs, err)
		} else if err := e.post(ctx, "/v1/logs", body); err != nil {
			errs = append(errs, err)
		}
	}
	if len(metrics) > 0 {
		body, err := marshalRequest(metricsResourceMetrics(metrics, e.serviceName))
		if err != nil {
			errs = append(errs, err)
		} else if err := e.post(ctx, "/v1/metrics", body); err != nil {
			errs = append(errs, err)
		}
	}
	return true, errors.Join(errs...)
}

func (e *exporter) takeLocked(limit int) ([]SpanData, []LogRecord, []MetricPoint) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var spans []SpanData
	var logs []LogRecord
	var metrics []MetricPoint
	spans, e.spans = takePrefix(e.spans, limit)
	logs, e.logs = takePrefix(e.logs, limit)
	metrics, e.metrics = takePrefix(e.metrics, limit)
	return spans, logs, metrics
}

func takePrefix[T any](items []T, limit int) ([]T, []T) {
	n := len(items)
	if n > limit {
		n = limit
	}
	if n == 0 {
		return nil, items
	}
	out := make([]T, n)
	copy(out, items[:n])
	return out, items[n:]
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

// resource names the process that produced a signal. service.name is the OTLP
// resource attribute collectors use to group a service (REQ-TELEM-015).
func resource(serviceName string) *resourcev1.Resource {
	if serviceName == "" {
		return nil
	}
	return &resourcev1.Resource{Attributes: []*commonv1.KeyValue{{
		Key:   "service.name",
		Value: &commonv1.AnyValue{Value: &commonv1.AnyValue_StringValue{StringValue: serviceName}},
	}}}
}

func traceResourceSpans(spans []SpanData, serviceName string) *tracev1.ResourceSpans {
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
		Resource:   resource(serviceName),
		ScopeSpans: []*tracev1.ScopeSpans{{Spans: out}},
	}
}

func logsResourceLogs(records []LogRecord, serviceName string) *logsv1.ResourceLogs {
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
		Resource:  resource(serviceName),
		ScopeLogs: []*logsv1.ScopeLogs{{LogRecords: out}},
	}
}

func metricsResourceMetrics(points []MetricPoint, serviceName string) *metricsv1.ResourceMetrics {
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
		Resource:     resource(serviceName),
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
