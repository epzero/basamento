package telemetry

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	commonv1 "go.opentelemetry.io/proto/otlp/common/v1"
	logsv1 "go.opentelemetry.io/proto/otlp/logs/v1"
	metricsv1 "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracev1 "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/epzero/basamento/core/coretest"
)

// waitFor spins until cond is true. It is a deadlock guard, not a timing
// assertion: it reads no clock, and it fails only when cond never becomes true.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 1_000_000; i++ {
		if cond() {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("timed out waiting for %s", what)
}

func signalReady(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// testCollector is a deterministic in-process OTLP/HTTP receiver.
type testCollector struct {
	server *httptest.Server

	mu        sync.Mutex
	fail      bool
	slow      chan struct{}
	received  chan struct{}
	requests  int
	confirmed int
	spans     []*tracev1.Span
	logs      []*logsv1.LogRecord
	metrics   []*metricsv1.Metric
	bodies    [][]byte
	resources map[string]map[string]string
}

func newTestCollector(t *testing.T) *testCollector {
	t.Helper()
	c := &testCollector{received: make(chan struct{}, 1), resources: map[string]map[string]string{}}
	c.server = httptest.NewServer(http.HandlerFunc(c.handle))
	t.Cleanup(c.server.Close)
	return c
}

func (c *testCollector) URL() string { return c.server.URL }

func (c *testCollector) handle(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	fail, slow := c.fail, c.slow
	c.requests++
	c.mu.Unlock()
	select {
	case c.received <- struct{}{}:
	default:
	}
	if slow != nil {
		select {
		case <-slow:
		case <-r.Context().Done():
			return
		}
	}
	body, _ := io.ReadAll(r.Body)
	if fail {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	c.mu.Lock()
	c.bodies = append(c.bodies, body)
	c.mu.Unlock()
	payload := requestPayload(body)
	switch r.URL.Path {
	case "/v1/traces":
		var msg tracev1.ResourceSpans
		if err := proto.Unmarshal(payload, &msg); err == nil {
			c.keepResource(r.URL.Path, msg.Resource.GetAttributes())
			for _, scope := range msg.ScopeSpans {
				c.mu.Lock()
				c.spans = append(c.spans, scope.Spans...)
				c.mu.Unlock()
			}
		}
	case "/v1/logs":
		var msg logsv1.ResourceLogs
		if err := proto.Unmarshal(payload, &msg); err == nil {
			c.keepResource(r.URL.Path, msg.Resource.GetAttributes())
			for _, scope := range msg.ScopeLogs {
				c.mu.Lock()
				c.logs = append(c.logs, scope.LogRecords...)
				c.mu.Unlock()
			}
		}
	case "/v1/metrics":
		var msg metricsv1.ResourceMetrics
		if err := proto.Unmarshal(payload, &msg); err == nil {
			c.keepResource(r.URL.Path, msg.Resource.GetAttributes())
			for _, scope := range msg.ScopeMetrics {
				c.mu.Lock()
				c.metrics = append(c.metrics, scope.Metrics...)
				c.mu.Unlock()
			}
		}
	}
	c.mu.Lock()
	c.confirmed++
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (c *testCollector) keepResource(path string, attrs []*commonv1.KeyValue) {
	values := make(map[string]string, len(attrs))
	for _, attr := range attrs {
		values[attr.Key] = attr.Value.GetStringValue()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resources[path] = values
}

func (c *testCollector) serviceNames() map[string]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]string, len(c.resources))
	for path, attrs := range c.resources {
		out[path] = attrs["service.name"]
	}
	return out
}

func requestPayload(body []byte) []byte {
	for len(body) > 0 {
		num, typ, n := protowire.ConsumeTag(body)
		if n < 0 {
			return nil
		}
		body = body[n:]
		switch typ {
		case protowire.BytesType:
			value, n := protowire.ConsumeBytes(body)
			if n < 0 {
				return nil
			}
			if num == 1 {
				return value
			}
			body = body[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, body)
			if n < 0 {
				return nil
			}
			body = body[n:]
		}
	}
	return nil
}

func (c *testCollector) setFail(fail bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail = fail
}

func (c *testCollector) counts() (spans, logs, metrics int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.spans), len(c.logs), len(c.metrics)
}

func (c *testCollector) requestCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests
}

func (c *testCollector) confirmedCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.confirmed
}

func (c *testCollector) logCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.logs)
}

func (c *testCollector) hasSpan(name string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, span := range c.spans {
		if span.Name == name {
			return true
		}
	}
	return false
}

// recordTestSpan registers and keeps a span whose attributes the test built
// directly, the way End does for a real span.
func recordTestSpan(t *testing.T, setup *Setup, span SpanData) {
	t.Helper()
	if !setup.beginEndedSpan() {
		t.Fatal("span refused before shutdown")
	}
	setup.acceptSpan(context.Background(), span)
	setup.endRecording()
}

func queuedCounts(e *exporter) (spans, logs, metrics int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.spans), len(e.logs), len(e.metrics)
}

func newCloudTestSetup(t *testing.T, clock *coretest.FakeClock, collector *testCollector) *Setup {
	t.Helper()
	setup, err := New(Config{
		Profile:      ProfileCloud,
		CollectorURL: collector.URL(),
		Clock:        clock,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		_ = setup.Shutdown(context.WithoutCancel(t.Context()))
	})
	return setup
}

func TestFlushExportsEverySignalOverOTLP(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	setup := newCloudTestSetup(t, clock, collector)

	ctx, span := setup.Tracer().Start(telemetryContext("acme", "run_01"), "step a")
	span.End()
	setup.Logger().InfoContext(ctx, "started")
	counter, _ := setup.Meter().Int64Counter("runs.started")
	counter.Add(ctx, 1)
	if err := setup.Flush(t.Context()); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	spans, logs, metrics := collector.counts()
	if spans != 1 || logs != 1 || metrics != 1 {
		t.Fatalf("collector got spans=%d logs=%d metrics=%d, want 1/1/1", spans, logs, metrics)
	}
	if collector.spans[0].Name != "step a" {
		t.Errorf("span name = %s", collector.spans[0].Name)
	}
	if body := collector.logs[0].Body.GetStringValue(); body != "started" {
		t.Errorf("log message = %q", body)
	}
	if collector.metrics[0].Name != "runs.started" {
		t.Errorf("metric name = %s", collector.metrics[0].Name)
	}
	if collector.metrics[0].GetSum().DataPoints[0].GetAsInt() != 1 {
		t.Errorf("metric value = %v", collector.metrics[0].GetSum().DataPoints[0].GetValue())
	}
}

func TestRecordingDoesNotWaitForTheCollector(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	block := make(chan struct{})
	collector.mu.Lock()
	collector.slow = block
	collector.mu.Unlock()
	setup := newCloudTestSetup(t, clock, collector)

	before := clock.Now()
	ctx, span := setup.Tracer().Start(telemetryContext("acme", "run_01"), "step a")
	span.End()
	setup.Logger().InfoContext(ctx, "started")
	counter, _ := setup.Meter().Int64Counter("runs.started")
	counter.Add(ctx, 1)
	if !clock.Now().Equal(before) {
		t.Fatalf("recording advanced the clock from %v to %v", before, clock.Now())
	}
	if got := collector.requestCount(); got != 0 {
		t.Fatalf("collector received %d requests while recording", got)
	}
	close(block)
}

func TestFailedExportBatchIsDroppedAndNotRetried(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	collector.setFail(true)
	setup := newCloudTestSetup(t, clock, collector)

	_, span := setup.Tracer().Start(telemetryContext("acme", "run_01"), "step a")
	span.End()
	setup.Logger().Info("started")
	counter, _ := setup.Meter().Int64Counter("runs.started")
	counter.Add(telemetryContext("acme", "run_01"), 1)

	if err := setup.ExportOnce(t.Context()); err == nil {
		t.Fatal("ExportOnce succeeded against a failing collector")
	}
	collector.setFail(false)
	if err := setup.Flush(t.Context()); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	spans, logs, metrics := collector.counts()
	if spans != 0 || logs != 0 || metrics != 0 {
		t.Fatalf("collector got spans=%d logs=%d metrics=%d after a dropped batch, want 0/0/0", spans, logs, metrics)
	}
}

func TestShutdownFlushesFinishedRecordingsThenRefusesNewOnes(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	setup := newCloudTestSetup(t, clock, collector)

	_, span := setup.Tracer().Start(telemetryContext("acme", "run_01"), "step a")
	span.End()
	setup.Logger().Info("started")
	counter, _ := setup.Meter().Int64Counter("runs.started")
	counter.Add(telemetryContext("acme", "run_01"), 1)

	if err := setup.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	spans, logs, metrics := collector.counts()
	if spans != 1 || logs != 1 || metrics != 1 {
		t.Fatalf("shutdown delivered spans=%d logs=%d metrics=%d, want 1/1/1", spans, logs, metrics)
	}

	ctx, late := setup.Tracer().Start(telemetryContext("acme", "run_01"), "late")
	late.End()
	setup.Logger().InfoContext(ctx, "late")
	counter.Add(ctx, 1)
	if err := setup.Flush(t.Context()); err != nil {
		t.Fatalf("Flush after shutdown: %v", err)
	}
	spans, logs, metrics = collector.counts()
	if spans != 1 || logs != 1 || metrics != 1 {
		t.Fatalf("collector got spans=%d logs=%d metrics=%d after shutdown, want unchanged", spans, logs, metrics)
	}
}

func TestRedactionHappensBeforeExport(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	setup := newCloudTestSetup(t, clock, collector)
	setup.SetCredential("sk-live-7f3a9c1b")

	setup.Logger().Info("carrying sk-live-7f3a9c1b")
	if err := setup.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := collector.logs[0].Body.GetStringValue(); got != Redacted {
		t.Errorf("exported message = %q, want redacted", got)
	}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	for _, body := range collector.bodies {
		if bytes.Contains(body, []byte("sk-live-7f3a9c1b")) {
			t.Fatal("an exported OTLP batch contains the credential value")
		}
	}
}

func TestExportCarriesServiceNameOnEverySignal(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	setup, err := New(Config{
		Profile:      ProfileCloud,
		CollectorURL: collector.URL(),
		Clock:        clock,
		ServiceName:  "saber",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := telemetryContext("acme", "run_01")
	_, span := setup.Tracer().Start(ctx, "step a")
	span.End()
	setup.Logger().InfoContext(ctx, "started")
	counter, _ := setup.Meter().Int64Counter("runs.started")
	counter.Add(ctx, 1)
	if err := setup.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	names := collector.serviceNames()
	for _, path := range []string{"/v1/traces", "/v1/logs", "/v1/metrics"} {
		if names[path] != "saber" {
			t.Errorf("%s service.name = %q, want saber", path, names[path])
		}
	}
}

func TestBackgroundExporterDrainsBacklogsLargerThanOneBatch(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	setup := newCloudTestSetup(t, clock, collector)

	const records = exportBatchSize + 300
	for i := 0; i < records; i++ {
		setup.Logger().Info("queued")
	}

	waitFor(t, "the size-triggered exporter to reach the collector", func() bool {
		return signalReady(collector.received)
	})
	if err := setup.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := collector.logCount(); got != records {
		t.Fatalf("collector received %d log records, want %d", got, records)
	}
}

func TestBackgroundExporterDoesNotBlockRecording(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	block := make(chan struct{})
	release := sync.OnceFunc(func() { close(block) })
	defer release()
	collector.mu.Lock()
	collector.slow = block
	collector.mu.Unlock()
	setup := newCloudTestSetup(t, clock, collector)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < exportTrigger+100; i++ {
			setup.Logger().Info("queued")
		}
	}()
	waitFor(t, "recording to finish without waiting for the exporter", func() bool {
		return signalReady(done)
	})

	release()
	if err := setup.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := collector.logCount(); got != exportTrigger+100 {
		t.Fatalf("collector received %d log records, want %d", got, exportTrigger+100)
	}
}

func TestShutdownWaitsForAnInFlightFlush(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	block := make(chan struct{})
	release := sync.OnceFunc(func() { close(block) })
	defer release()
	collector.mu.Lock()
	collector.slow = block
	collector.mu.Unlock()
	setup := newCloudTestSetup(t, clock, collector)
	setup.Logger().Info("queued")

	ctx := context.Background()
	flushDone := make(chan error, 1)
	go func() { flushDone <- setup.Flush(ctx) }()
	waitFor(t, "the flush to reach the collector", func() bool {
		return signalReady(collector.received)
	})

	var shutdownReturned atomic.Bool
	var shutdownConfirmed atomic.Int64
	shutdownDone := make(chan error, 1)
	go func() {
		err := setup.Shutdown(ctx)
		shutdownConfirmed.Store(int64(collector.confirmedCount()))
		shutdownReturned.Store(true)
		shutdownDone <- err
	}()

	// The spin is a deadlock guard, not a timing assertion: a Shutdown that
	// respects an in-flight Flush cannot return until the channel is released.
	for i := 0; i < 100000 && !shutdownReturned.Load(); i++ {
		runtime.Gosched()
	}
	if shutdownReturned.Load() {
		t.Fatal("Shutdown returned while a Flush still held the batch")
	}

	release()
	if err := <-flushDone; err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if shutdownConfirmed.Load() == 0 {
		t.Fatal("Shutdown returned before the collector confirmed the batch")
	}
}

func TestConcurrentShutdownWaitsForDelivery(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	block := make(chan struct{})
	release := sync.OnceFunc(func() { close(block) })
	defer release()
	collector.mu.Lock()
	collector.slow = block
	collector.mu.Unlock()
	setup := newCloudTestSetup(t, clock, collector)
	setup.Logger().Info("queued")

	ctx := context.Background()
	firstDone := make(chan error, 1)
	go func() { firstDone <- setup.Shutdown(ctx) }()
	waitFor(t, "the first shutdown to reach the collector", func() bool {
		return signalReady(collector.received)
	})

	var secondReturned atomic.Bool
	var secondConfirmed atomic.Int64
	secondDone := make(chan error, 1)
	go func() {
		err := setup.Shutdown(ctx)
		secondConfirmed.Store(int64(collector.confirmedCount()))
		secondReturned.Store(true)
		secondDone <- err
	}()
	for i := 0; i < 100000 && !secondReturned.Load(); i++ {
		runtime.Gosched()
	}
	if secondReturned.Load() {
		t.Fatal("the second Shutdown returned before delivery finished")
	}

	release()
	if err := <-firstDone; err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
	if secondConfirmed.Load() == 0 {
		t.Fatal("the second Shutdown returned before the collector confirmed the batch")
	}
}

func TestRedactionCoversErrorsStructsAndBytesBeforeExport(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	setup := newCloudTestSetup(t, clock, collector)
	setup.SetCredential("sk-live-7f3a9c1b")

	type credentials struct {
		Name     string
		Password string
	}
	deep := &innerCreds{Password: "S3cretField"}
	setup.Logger().Info("structured",
		slog.Any("error", errors.New("failed with sk-live-7f3a9c1b")),
		slog.Any("credentials", credentials{Name: "ana", Password: "hunter2"}),
		slog.Any("raw", []byte("sk-live-7f3a9c1b")),
		slog.Any("stringerField", stringerCredential{Password: "S3cretField"}),
		slog.Any("errorField", errorCredential{Secret: "S3cretField"}),
		slog.Any("wrapped", fmt.Errorf("auth: %w", errorCredential{Secret: "S3cretField"})),
		slog.Any("joined", errors.Join(errors.New("plain"), errorCredential{Secret: "S3cretField"})),
		slog.Any("pointerHolder", pointerHolder{inner: &innerCreds{Password: "S3cretField"}}),
		slog.Any("embeddedHolder", embeddedErrorHolder{errorCredential{Secret: "S3cretField"}}),
		slog.Any("plainWrapper", plainWrapper{cause: errorCredential{Secret: "S3cretField"}}),
		slog.Any("mapStringer", mapStringer{"password": "S3cretField", "note": "kept"}),
		slog.Any("sliceError", sliceError{errorCredential{Secret: "S3cretField"}}),
		slog.Any("doublePointer", doublePointerHolder{inner: &deep}),
		slog.Any("structMap", structMapStringer{m: map[string]string{"password": "S3cretField"}}),
		slog.Any("intKeyMap", intKeyMapStringer{m: map[int]string{1: "S3cretField"}}),
	)
	// OTel attribute values are primitives, so the structured maps go through
	// the same redactor at the record boundary and then the OTLP encoder.
	structured := setup.redact.Map(map[string]any{
		"stringerField":  stringerCredential{Password: "S3cretField"},
		"errorField":     errorCredential{Secret: "S3cretField"},
		"pointerHolder":  pointerHolder{inner: &innerCreds{Password: "S3cretField"}},
		"embeddedHolder": embeddedErrorHolder{errorCredential{Secret: "S3cretField"}},
		"plainWrapper":   plainWrapper{cause: errorCredential{Secret: "S3cretField"}},
		"mapStringer":    mapStringer{"password": "S3cretField", "note": "kept"},
		"sliceError":     sliceError{errorCredential{Secret: "S3cretField"}},
		"doublePointer":  doublePointerHolder{inner: &deep},
		"structMap":      structMapStringer{m: map[string]string{"password": "S3cretField"}},
		"intKeyMap":      intKeyMapStringer{m: map[int]string{1: "S3cretField"}},
	})
	recordTestSpan(t, setup, SpanData{
		TraceID:    "0102030405060708090a0b0c0d0e0f10",
		SpanID:     "0102030405060708",
		Name:       "redaction",
		Start:      clock.Now(),
		End:        clock.Now(),
		Status:     StatusOK,
		Attributes: structured,
	})
	setup.appendMetric(MetricPoint{
		Name:   "redaction",
		Kind:   KindCounter,
		Value:  1,
		IsInt:  true,
		Time:   clock.Now(),
		Labels: structured,
	})
	if err := setup.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	spans, logs, metrics := collector.counts()
	if spans == 0 || logs == 0 || metrics == 0 {
		t.Fatalf("collector got spans=%d logs=%d metrics=%d, want all three", spans, logs, metrics)
	}
	for _, secret := range []string{"sk-live-7f3a9c1b", "hunter2", "S3cretField"} {
		if collectorLeaks(collector, secret) {
			t.Fatalf("an exported OTLP signal contains %q", secret)
		}
	}
}

func collectorLeaks(collector *testCollector, text string) bool {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	for _, rec := range collector.logs {
		if anyValueContains(rec.Body, text) || keyValuesContain(rec.Attributes, text) {
			return true
		}
	}
	for _, span := range collector.spans {
		if keyValuesContain(span.Attributes, text) {
			return true
		}
	}
	for _, metric := range collector.metrics {
		for _, attrs := range metricAttributes(metric) {
			if keyValuesContain(attrs, text) {
				return true
			}
		}
	}
	return false
}

func anyValueContains(value *commonv1.AnyValue, text string) bool {
	if value == nil {
		return false
	}
	if strings.Contains(value.GetStringValue(), text) {
		return true
	}
	if array := value.GetArrayValue(); array != nil {
		for _, item := range array.Values {
			if anyValueContains(item, text) {
				return true
			}
		}
	}
	if list := value.GetKvlistValue(); list != nil {
		for _, item := range list.Values {
			if strings.Contains(item.Key, text) || anyValueContains(item.Value, text) {
				return true
			}
		}
	}
	return false
}

func keyValuesContain(items []*commonv1.KeyValue, text string) bool {
	for _, item := range items {
		if strings.Contains(item.Key, text) || anyValueContains(item.Value, text) {
			return true
		}
	}
	return false
}

func metricAttributes(metric *metricsv1.Metric) [][]*commonv1.KeyValue {
	var out [][]*commonv1.KeyValue
	if sum := metric.GetSum(); sum != nil {
		for _, point := range sum.DataPoints {
			out = append(out, point.Attributes)
		}
	}
	if gauge := metric.GetGauge(); gauge != nil {
		for _, point := range gauge.DataPoints {
			out = append(out, point.Attributes)
		}
	}
	if histogram := metric.GetHistogram(); histogram != nil {
		for _, point := range histogram.DataPoints {
			out = append(out, point.Attributes)
		}
	}
	return out
}

// TestShutdownDeliversASpanThatEndsDuringTheDrain covers the window where a
// span started before Shutdown ends while the final drain is in progress. Its
// recording finishes before Shutdown returns, so REQ-TELEM-022 delivers it.
func TestShutdownDeliversASpanThatEndsDuringTheDrain(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	block := make(chan struct{})
	release := sync.OnceFunc(func() { close(block) })
	defer release()
	collector.mu.Lock()
	collector.slow = block
	collector.mu.Unlock()
	setup := newCloudTestSetup(t, clock, collector)

	_, span := setup.Tracer().Start(telemetryContext("acme", "run_01"), "in flight")
	for i := 0; i < exportTrigger; i++ {
		setup.Logger().Info("queued")
	}
	waitFor(t, "the background drain to reach the collector", func() bool {
		return signalReady(collector.received)
	})

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- setup.Shutdown(context.Background()) }()
	waitFor(t, "Shutdown to close the setup", setup.isClosed)

	// End lands after Shutdown started and while it is draining.
	span.End()
	release()

	waitFor(t, "Shutdown to return", func() bool {
		select {
		case err := <-shutdownDone:
			if err != nil {
				t.Errorf("Shutdown: %v", err)
			}
			return true
		default:
			return false
		}
	})
	if !collector.hasSpan("in flight") {
		t.Fatal("the span that ended during the drain was not delivered")
	}
}

// TestExportQueuesAreBounded proves the queue bound under a stalled collector:
// each per-type queue stays at exportQueueCap and the drops are counted.
func TestExportQueuesAreBounded(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	block := make(chan struct{})
	release := sync.OnceFunc(func() { close(block) })
	defer release()
	collector.mu.Lock()
	collector.slow = block
	collector.mu.Unlock()
	setup := newCloudTestSetup(t, clock, collector)

	// The background exporter can remove one batch before its stalled request
	// blocks, so overfill past the cap by more than that.
	const records = exportQueueCap + 2*exportBatchSize
	for i := 0; i < records; i++ {
		setup.Logger().Info("queued")
	}
	_, logs, _ := queuedCounts(setup.export)
	if logs > exportQueueCap {
		t.Fatalf("log queue holds %d records, want at most %d", logs, exportQueueCap)
	}
	if got := setup.DroppedExports(); got == 0 {
		t.Fatal("a full queue dropped records without counting them")
	}
	release()
	if err := setup.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

// TestSmallBacklogsExportAfterTheInterval covers a long-lived process whose
// pending signals never reach exportTrigger: the first enqueue after the
// oldest signal has waited exportInterval on the setup clock wakes the drain.
func TestSmallBacklogsExportAfterTheInterval(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	setup := newCloudTestSetup(t, clock, collector)

	setup.Logger().Info("first")
	if got := collector.requestCount(); got != 0 {
		t.Fatalf("the collector received %d requests below the trigger, want 0", got)
	}
	clock.Advance(exportInterval)
	setup.Logger().Info("second")

	waitFor(t, "the age-triggered export", func() bool {
		return collector.logCount() == 2
	})
}

// TestShutdownReturnsWhenItsContextIsAlreadyCancelled proves the context is
// consulted even while the owned goroutine has not exited.
func TestShutdownReturnsWhenItsContextIsAlreadyCancelled(t *testing.T) {
	e := &exporter{
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		bgCancel: func() {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.halt(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("halt = %v, want context.Canceled", err)
	}
}

// TestShutdownWithACancelledDeadlineDropsTheBacklog proves a stalled collector
// cannot hold Shutdown past a cancelled context.
func TestShutdownWithACancelledDeadlineDropsTheBacklog(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	block := make(chan struct{})
	release := sync.OnceFunc(func() { close(block) })
	defer release()
	collector.mu.Lock()
	collector.slow = block
	collector.mu.Unlock()
	setup := newCloudTestSetup(t, clock, collector)

	for i := 0; i < 3*exportBatchSize; i++ {
		setup.Logger().Info("queued")
	}
	waitFor(t, "the background drain to reach the collector", func() bool {
		return signalReady(collector.received)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := setup.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Shutdown = %v, want context.Canceled", err)
	}
	release()
}

// expiredContext reports an expired deadline through Err only, leaving Done
// open. That makes Shutdown's halt take the already-stopped path and its loop
// reach the final expired-deadline return deterministically, without a select
// race between a closed done channel and a closed context.
type expiredContext struct{ context.Context }

func (expiredContext) Done() <-chan struct{} { return nil }
func (expiredContext) Err() error            { return context.DeadlineExceeded }

// TestShutdownRefusesASpanThatEndsAfterAnExpiredDeadline covers the return
// that gives up on its context: it must drain nothing further but still mark
// the setup shut down, so a span ending afterwards is not accepted into an
// exporter whose goroutine has stopped.
func TestShutdownRefusesASpanThatEndsAfterAnExpiredDeadline(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	collector := newTestCollector(t)
	setup := newCloudTestSetup(t, clock, collector)

	// Stop the owned goroutine first so halt returns without a select race and
	// the queued signals are still pending when the loop sees the deadline.
	if err := setup.export.halt(context.Background()); err != nil {
		t.Fatalf("stopping the exporter: %v", err)
	}
	_, span := setup.Tracer().Start(telemetryContext("acme", "run_01"), "after expiry")
	for i := 0; i < 3; i++ {
		setup.Logger().Info("queued")
	}

	err := setup.Shutdown(expiredContext{Context: context.Background()})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown = %v, want context.DeadlineExceeded", err)
	}

	span.End()
	if got := len(setup.Spans()); got != 0 {
		t.Fatalf("kept %d spans after the expired deadline, want 0", got)
	}
	spans, _, _ := queuedCounts(setup.export)
	if spans != 0 {
		t.Fatalf("the export queue accepted %d spans after the expired deadline, want 0", spans)
	}
}

func TestAnyValueEncodesLargeUnsignedAndPreEpochTimes(t *testing.T) {
	if got := anyValue(uint64(math.MaxInt64)).GetIntValue(); got != math.MaxInt64 {
		t.Errorf("MaxInt64 as uint64 = %d, want %d", got, int64(math.MaxInt64))
	}
	if got := anyValue(uint64(math.MaxInt64) + 1); got.GetIntValue() != 0 || got.GetDoubleValue() <= 0 {
		t.Errorf("uint64 above MaxInt64 = %v, want a positive double, not a wrapped int", got)
	}
	if got := anyValue(uint(math.MaxInt64)).GetIntValue(); got != math.MaxInt64 {
		t.Errorf("MaxInt64 as uint = %d, want %d", got, int64(math.MaxInt64))
	}
	before := time.Date(1960, 1, 1, 0, 0, 0, 0, time.UTC)
	if got := unixNano(before); got != 0 {
		t.Errorf("pre-1970 time = %d, want 0", got)
	}
	after := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if got, want := unixNano(after), uint64(after.UnixNano()); got != want {
		t.Errorf("post-1970 time = %d, want %d", got, want)
	}
}

func TestMarshalRequestIsAnOTLPExportRequest(t *testing.T) {
	body, err := marshalRequest(&tracev1.ResourceSpans{})
	if err != nil {
		t.Fatalf("marshalRequest: %v", err)
	}
	if len(body) == 0 || !strings.HasPrefix(string(body), "\x0a") {
		t.Fatalf("body does not start with field 1: %x", body)
	}
	payload := requestPayload(body)
	var msg tracev1.ResourceSpans
	if err := proto.Unmarshal(payload, &msg); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
}
