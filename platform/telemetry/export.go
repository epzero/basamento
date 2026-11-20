package telemetry

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcev1 "go.opentelemetry.io/proto/otlp/resource/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/epzero/basamento/core"
)

const (
	// exportBatchSize bounds one export request per signal type. Delivery
	// drains a larger queue in several bounded batches.
	exportBatchSize = 1024
	// exportTrigger is the number of pending signals that wakes the background
	// exporter.
	exportTrigger = 512
	// exportQueueCap bounds each per-type queue. Past the cap the oldest
	// pending signal of that type is dropped and counted, so a stalled
	// collector cannot grow the process without limit (REQ-TELEM-016).
	exportQueueCap = 4096
	// exportInterval is how long the oldest pending signal may wait before the
	// next enqueue wakes the exporter. It is read from the setup clock, never
	// the wall clock, so a test advances the fake clock to trigger it.
	exportInterval = time.Second
)

// exporter is a non-blocking OTLP/HTTP queue with an owned delivery goroutine.
// Recording only appends and wakes the goroutine at exportTrigger, so a slow or
// unreachable collector never delays the work (REQ-TELEM-016). A batch that
// cannot be delivered is removed and not retried (REQ-TELEM-017).
type exporter struct {
	endpoint    string
	serviceName string
	clock       core.Clock
	client      *http.Client

	// deliverMu serialises every delivery attempt, so Flush, Shutdown, an
	// explicit ExportOnce and the background goroutine never overlap. A
	// shutdown that returns therefore has no delivery still in flight.
	deliverMu sync.Mutex

	mu           sync.Mutex
	spans        []SpanData
	logs         []LogRecord
	metrics      []MetricPoint
	pendingSince time.Time

	dropped  atomic.Int64
	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	stopOnce sync.Once
	bgCancel context.CancelFunc
}

func newExporter(endpoint, serviceName string, clock core.Clock) *exporter {
	e := &exporter{
		endpoint:    strings.TrimRight(endpoint, "/"),
		serviceName: serviceName,
		clock:       clock,
		client:      &http.Client{Timeout: 5 * time.Second},
		wake:        make(chan struct{}, 1),
		stop:        make(chan struct{}),
		done:        make(chan struct{}),
	}
	bg, cancel := context.WithCancel(context.Background())
	e.bgCancel = cancel
	go e.run(bg)
	return e
}

// run is the owned delivery goroutine. It drains when woken and stops between
// batches once Shutdown closes stop; the batch it has already taken is still
// delivered. A cancelled background context aborts a request that outlives the
// caller's shutdown deadline.
func (e *exporter) run(ctx context.Context) {
	defer close(e.done)
	for {
		select {
		case <-e.stop:
			return
		case <-e.wake:
			_ = e.drainUntil(ctx, e.stop)
		}
	}
}

func (e *exporter) enqueueSpan(span SpanData) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var dropped bool
	e.spans, dropped = appendBounded(e.spans, span, exportQueueCap)
	if dropped {
		e.dropped.Add(1)
	}
	e.notifyLocked()
}

func (e *exporter) enqueueLog(rec LogRecord) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var dropped bool
	e.logs, dropped = appendBounded(e.logs, rec, exportQueueCap)
	if dropped {
		e.dropped.Add(1)
	}
	e.notifyLocked()
}

func (e *exporter) enqueueMetric(point MetricPoint) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var dropped bool
	e.metrics, dropped = appendBounded(e.metrics, point, exportQueueCap)
	if dropped {
		e.dropped.Add(1)
	}
	e.notifyLocked()
}

// appendBounded appends item, dropping the oldest when the queue is full. It
// reports whether it dropped a signal.
func appendBounded[T any](items []T, item T, limit int) ([]T, bool) {
	if len(items) >= limit {
		copy(items, items[1:])
		items[len(items)-1] = item
		return items, true
	}
	return append(items, item), false
}

// notifyLocked wakes the delivery goroutine at the size trigger, or when the
// oldest pending signal has waited exportInterval on the setup clock. The age
// check runs on enqueue, so it reads no wall clock and needs no timer: an idle
// process exports its backlog with its next signal.
func (e *exporter) notifyLocked() {
	// The caller has just appended, so the queue is never empty here;
	// takeLocked resets pendingSince when a drain empties it.
	now := e.clock.Now()
	if e.pendingSince.IsZero() {
		e.pendingSince = now
	}
	if len(e.spans)+len(e.logs)+len(e.metrics) < exportTrigger && now.Sub(e.pendingSince) < exportInterval {
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

// halt stops the owned goroutine and waits for it to exit. A delivery it has in
// flight is allowed to finish, so an accepted batch is not discarded; ctx
// bounds the wait, and on expiry the background request is cancelled and the
// owner returns ctx.Err() without being held past its deadline. It is safe to
// call more than once and from several goroutines.
func (e *exporter) halt(ctx context.Context) error {
	e.stopOnce.Do(func() { close(e.stop) })
	select {
	case <-e.done:
		e.bgCancel()
		return nil
	case <-ctx.Done():
		// Cancelling the background context aborts the request it has in
		// flight; its drain then returns (it checks the context before every
		// batch) and the goroutine exits on its own. Waiting for it here would
		// hold the caller past the deadline it just exceeded.
		e.bgCancel()
		return ctx.Err()
	}
}

// drain delivers every pending signal in bounded batches. A failed batch is
// dropped; the remaining batches are still attempted (REQ-TELEM-017). The
// context is checked before every batch and on every request, so a cancelled
// deadline drops the rest instead of working through the whole backlog.
func (e *exporter) drain(ctx context.Context) error {
	return e.drainUntil(ctx, nil)
}

// drainUntil is drain with an optional stop channel: the owned goroutine uses
// it to leave the remaining batches to Shutdown instead of draining the whole
// backlog itself.
func (e *exporter) drainUntil(ctx context.Context, stop <-chan struct{}) error {
	e.deliverMu.Lock()
	defer e.deliverMu.Unlock()
	var errs []error
	for {
		if stop != nil {
			select {
			case <-stop:
				return errors.Join(errs...)
			default:
			}
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		more, err := e.deliverBatchLocked(ctx)
		if err != nil {
			errs = append(errs, err)
		}
		if !more {
			return errors.Join(errs...)
		}
	}
}

// pending reports how many signals wait in the queues.
func (e *exporter) pending() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.spans) + len(e.logs) + len(e.metrics)
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
	if len(e.spans)+len(e.logs)+len(e.metrics) == 0 {
		e.pendingSince = time.Time{}
	}
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
		return uintValue(uint64(typed))
	case uint32:
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: int64(typed)}}
	case uint64:
		return uintValue(typed)
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

// uintValue encodes a uint64 exactly when it fits an OTLP int. A larger value
// has no signed representation, so it is exported as a double instead of
// wrapping to a negative int.
func uintValue(value uint64) *commonv1.AnyValue {
	if value <= math.MaxInt64 {
		return &commonv1.AnyValue{Value: &commonv1.AnyValue_IntValue{IntValue: int64(value)}}
	}
	return &commonv1.AnyValue{Value: &commonv1.AnyValue_DoubleValue{DoubleValue: float64(value)}}
}

func decodeID(id string) []byte {
	raw, err := hex.DecodeString(id)
	if err != nil {
		return nil
	}
	return raw
}

// unixNano encodes t as OTLP's unsigned nanoseconds since the epoch. A zero or
// pre-1970 time has no unsigned representation, so it is clamped to 0 instead
// of wrapping to a huge value.
func unixNano(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	nanos := t.UnixNano()
	if nanos < 0 {
		return 0
	}
	return uint64(nanos)
}
