// Package telemtest is the deterministic OTLP collector used by the BDD
// telemetry scenarios. It never reaches a vendor: it receives OTLP/HTTP
// protobuf batches in process and records what arrived.
package telemtest

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"
)

// Span is one span the collector received.
type Span struct {
	Name         string
	TraceID      string
	SpanID       string
	ParentSpanID string
	Status       string
	Start        time.Time
	End          time.Time
}

// LogRecord is one log record the collector received.
type LogRecord struct {
	Message string
	Attrs   map[string]string
}

// MetricPoint is one metric point the collector received.
type MetricPoint struct {
	Name  string
	Value float64
	IsInt bool
}

// Collector is an in-process OTLP/HTTP receiver.
type Collector struct {
	server *httptest.Server

	mu          sync.Mutex
	unreachable bool
	release     chan struct{}
	requests    int
	confirmed   int
	spans       []Span
	logs        []LogRecord
	metrics     []MetricPoint
	bodies      [][]byte
}

// New starts a collector and registers its shutdown with the caller.
func New() *Collector {
	c := &Collector{}
	c.server = httptest.NewServer(http.HandlerFunc(c.handle))
	return c
}

// URL is the base URL to configure as an OTLP collector.
func (c *Collector) URL() string { return c.server.URL }

// Close stops the receiver.
func (c *Collector) Close() {
	c.ReleaseSlow()
	c.server.Close()
}

// SetUnreachable makes every export attempt answer 503 without keeping the
// batch. Clearing it makes the collector reachable again.
func (c *Collector) SetUnreachable(unreachable bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.unreachable = unreachable
}

// SetSlow makes export attempts wait until ReleaseSlow. The wait is a channel,
// not wall time: a test can prove recording never touches the collector.
func (c *Collector) SetSlow(answerAfter time.Duration) {
	_ = answerAfter // the gate proves non-blocking, never that wall time passed
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.release == nil {
		c.release = make(chan struct{})
	}
}

// ReleaseSlow lets a slow collector answer.
func (c *Collector) ReleaseSlow() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.release != nil {
		close(c.release)
		c.release = nil
	}
}

// Spans returns the spans received so far.
func (c *Collector) Spans() []Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Span, len(c.spans))
	copy(out, c.spans)
	return out
}

// Logs returns the log records received so far.
func (c *Collector) Logs() []LogRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]LogRecord, len(c.logs))
	copy(out, c.logs)
	return out
}

// Metrics returns the metric points received so far.
func (c *Collector) Metrics() []MetricPoint {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]MetricPoint, len(c.metrics))
	copy(out, c.metrics)
	return out
}

// Requests reports how many export requests reached the collector, including
// the ones it refused.
func (c *Collector) Requests() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests
}

// Confirmed reports how many export requests the collector answered with 200.
func (c *Collector) Confirmed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.confirmed
}

// Contains reports whether any received batch body contains text.
func (c *Collector) Contains(text string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, body := range c.bodies {
		for i := 0; i+len(text) <= len(body); i++ {
			if string(body[i:i+len(text)]) == text {
				return true
			}
		}
	}
	return false
}

func (c *Collector) handle(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	unreachable, release := c.unreachable, c.release
	c.requests++
	c.mu.Unlock()

	if release != nil {
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
	}
	if unreachable {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	c.record(r.URL.Path, requestPayload(body), body)
	c.mu.Lock()
	c.confirmed++
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (c *Collector) record(path string, payload, raw []byte) {
	c.mu.Lock()
	c.bodies = append(c.bodies, raw)
	c.mu.Unlock()

	switch path {
	case "/v1/traces":
		var msg tracev1.ResourceSpans
		if proto.Unmarshal(payload, &msg) != nil {
			return
		}
		for _, scope := range msg.ScopeSpans {
			for _, span := range scope.Spans {
				c.mu.Lock()
				c.spans = append(c.spans, Span{
					Name:         span.Name,
					TraceID:      hexID(span.TraceId),
					SpanID:       hexID(span.SpanId),
					ParentSpanID: hexID(span.ParentSpanId),
					Status:       statusName(span.GetStatus().GetCode()),
					Start:        time.Unix(0, int64(span.StartTimeUnixNano)).UTC(),
					End:          time.Unix(0, int64(span.EndTimeUnixNano)).UTC(),
				})
				c.mu.Unlock()
			}
		}
	case "/v1/logs":
		var msg logsv1.ResourceLogs
		if proto.Unmarshal(payload, &msg) != nil {
			return
		}
		for _, scope := range msg.ScopeLogs {
			for _, rec := range scope.LogRecords {
				c.mu.Lock()
				c.logs = append(c.logs, LogRecord{
					Message: rec.Body.GetStringValue(),
					Attrs:   attrs(rec.Attributes),
				})
				c.mu.Unlock()
			}
		}
	case "/v1/metrics":
		var msg metricsv1.ResourceMetrics
		if proto.Unmarshal(payload, &msg) != nil {
			return
		}
		for _, scope := range msg.ScopeMetrics {
			for _, metric := range scope.Metrics {
				for _, point := range numberPoints(metric) {
					c.mu.Lock()
					c.metrics = append(c.metrics, MetricPoint{Name: metric.Name, Value: point.value, IsInt: point.isInt})
					c.mu.Unlock()
				}
			}
		}
	}
}

type numberPoint struct {
	value float64
	isInt bool
}

func numberPoints(metric *metricsv1.Metric) []numberPoint {
	var out []numberPoint
	if sum := metric.GetSum(); sum != nil {
		for _, point := range sum.DataPoints {
			out = append(out, pointValue(point))
		}
	}
	if gauge := metric.GetGauge(); gauge != nil {
		for _, point := range gauge.DataPoints {
			out = append(out, pointValue(point))
		}
	}
	if histogram := metric.GetHistogram(); histogram != nil {
		for _, point := range histogram.DataPoints {
			out = append(out, numberPoint{value: point.GetSum()})
		}
	}
	return out
}

func pointValue(point *metricsv1.NumberDataPoint) numberPoint {
	if value, ok := point.Value.(*metricsv1.NumberDataPoint_AsInt); ok {
		return numberPoint{value: float64(value.AsInt), isInt: true}
	}
	return numberPoint{value: point.GetAsDouble()}
}

func requestPayload(body []byte) []byte {
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			return nil
		}
		body = body[n:]
		if typ != protowire.BytesType {
			size := protowire.ConsumeFieldValue(num, typ, body)
			if size < 0 {
				return nil
			}
			body = body[size:]
			continue
		}
		value, size := protowire.ConsumeBytes(body)
		if size < 0 {
			return nil
		}
		if num == 1 {
			return value
		}
		body = body[size:]
	}
	return nil
}

func attrs(items []*commonv1.KeyValue) map[string]string {
	if len(items) == 0 {
		return nil
	}
	out := make(map[string]string, len(items))
	for _, item := range items {
		out[item.Key] = valueString(item.Value)
	}
	return out
}

func valueString(value *commonv1.AnyValue) string {
	if value == nil {
		return ""
	}
	switch typed := value.Value.(type) {
	case *commonv1.AnyValue_StringValue:
		return typed.StringValue
	case *commonv1.AnyValue_BoolValue:
		if typed.BoolValue {
			return "true"
		}
		return "false"
	case *commonv1.AnyValue_IntValue:
		return strconv.FormatInt(typed.IntValue, 10)
	case *commonv1.AnyValue_DoubleValue:
		return strconv.FormatFloat(typed.DoubleValue, 'g', -1, 64)
	default:
		return ""
	}
}

func hexID(raw []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(raw)*2)
	for _, b := range raw {
		out = append(out, digits[b>>4], digits[b&0xf])
	}
	return string(out)
}

func statusName(code tracev1.Status_StatusCode) string {
	switch code {
	case tracev1.Status_STATUS_CODE_OK:
		return "ok"
	case tracev1.Status_STATUS_CODE_ERROR:
		return "error"
	default:
		return "unset"
	}
}
