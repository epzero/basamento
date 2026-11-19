package telemetry

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
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
		<-slow
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
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = setup.Shutdown(ctx)
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

	select {
	case <-collector.received:
	case <-time.After(5 * time.Second):
		t.Fatal("the size-triggered exporter never reached the collector")
	}
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
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("recording waited for the background exporter")
	}

	close(block)
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
	collector.mu.Lock()
	collector.slow = block
	collector.mu.Unlock()
	setup := newCloudTestSetup(t, clock, collector)
	setup.Logger().Info("queued")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	flushDone := make(chan error, 1)
	go func() { flushDone <- setup.Flush(ctx) }()
	<-collector.received

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

	close(block)
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
	collector.mu.Lock()
	collector.slow = block
	collector.mu.Unlock()
	setup := newCloudTestSetup(t, clock, collector)
	setup.Logger().Info("queued")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstDone := make(chan error, 1)
	go func() { firstDone <- setup.Shutdown(ctx) }()
	<-collector.received

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

	close(block)
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
	setup.Logger().Info("structured",
		slog.Any("error", errors.New("failed with sk-live-7f3a9c1b")),
		slog.Any("credentials", credentials{Name: "ana", Password: "hunter2"}),
		slog.Any("raw", []byte("sk-live-7f3a9c1b")),
	)
	if err := setup.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	collector.mu.Lock()
	defer collector.mu.Unlock()
	if len(collector.bodies) == 0 {
		t.Fatal("collector received no OTLP batch")
	}
	for _, body := range collector.bodies {
		for _, secret := range []string{"sk-live-7f3a9c1b", "hunter2"} {
			if bytes.Contains(body, []byte(secret)) {
				t.Fatalf("an exported OTLP batch contains %q", secret)
			}
		}
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
