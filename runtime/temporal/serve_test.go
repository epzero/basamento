package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// In-flight workflow and activity tasks get 30 s to finish when Serve shuts down.
func TestWorkerStopTimeoutIs30Seconds(t *testing.T) {
	if workerStopTimeout != 30*time.Second {
		t.Fatalf("workerStopTimeout=%v want 30s", workerStopTimeout)
	}
}

func testServeRuntime(t *testing.T, fc *fakeClient, fw *fakeWorker) *Runtime {
	t.Helper()
	rt, err := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	if err != nil {
		t.Fatal(err)
	}
	_ = rt.RegisterFlowHost(func(runtime.DurableContext, runtime.FlowRef, json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	})
	rt.dial = func(opts client.Options) (client.Client, error) {
		fc.recordDial(opts)
		return fc, nil
	}
	if fw != nil {
		fw.mu.Lock()
		if fw.started == nil {
			fw.started = make(chan struct{})
		}
		fw.mu.Unlock()
		rt.newWorker = func(_ client.Client, queue string, opts worker.Options) worker.Worker {
			fw.mu.Lock()
			fw.queue = queue
			fw.opts = opts
			fw.mu.Unlock()
			return fw
		}
	}
	return rt
}

func waitWorkerStarted(t *testing.T, started <-chan struct{}, done <-chan error) {
	t.Helper()
	if started == nil {
		t.Fatal("worker started channel unset")
	}
	if done == nil {
		<-started
		return
	}
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("Serve returned %v before the worker started", err)
	}
}

func TestServeHappyPath(t *testing.T) {
	fc := &fakeClient{}
	fw := &fakeWorker{started: make(chan struct{})}
	rt := testServeRuntime(t, fc, fw)
	ctx, cancel := context.WithCancel(context.Background())
	started := fw.started
	done := make(chan error, 1)
	go func() { done <- rt.Serve(ctx) }()
	waitWorkerStarted(t, started, done)
	wantOps := []string{"get_system_info", "list_search_attributes", "add_search_attributes"}
	gotOps := fc.ServeOps()
	if len(gotOps) < len(wantOps) {
		t.Fatalf("serve ops: got %v", gotOps)
	}
	for i, w := range wantOps {
		if gotOps[i] != w {
			t.Fatalf("op[%d]: got %q want %q (all %v)", i, gotOps[i], w, gotOps)
		}
	}
	q, opts, wfs, acts, startCnt, _ := fw.snapshot()
	if q != DefaultTaskQueue {
		t.Fatalf("queue: got %q", q)
	}
	if opts.WorkflowPanicPolicy != worker.BlockWorkflow || opts.WorkerStopTimeout != workerStopTimeout || opts.DeadlockDetectionTimeout != 0 {
		t.Fatalf("worker opts: %#v", opts)
	}
	if len(wfs) != 1 || wfs[0] != HostWorkflowName {
		t.Fatalf("workflows: %v", wfs)
	}
	wantActs := []string{stepActivityName, emitActivityName}
	if len(acts) != len(wantActs) {
		t.Fatalf("activities: %v", acts)
	}
	for i, a := range wantActs {
		if acts[i] != a {
			t.Fatalf("activity[%d]: got %q want %q", i, acts[i], a)
		}
	}
	if startCnt != 1 {
		t.Fatalf("start count: %d", startCnt)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned %v want nil", err)
	}
	_, _, _, _, _, stops := fw.snapshot()
	if stops != 1 {
		t.Fatalf("stop count: %d", stops)
	}
	if fc.CloseCount() != 1 {
		t.Fatalf("client close count: %d", fc.CloseCount())
	}
	ctxTenant := core.WithTenant(context.Background(), "acme")
	if _, err := rt.Start(ctxTenant, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}}); core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("after Serve: %v", err)
	}
}

func TestServeUnsupportedVersionBeforeWorker(t *testing.T) {
	fc := &fakeClient{}
	fc.wfSvc = &fakeWorkflowService{parent: fc, version: "1.33.0"}
	fw := &fakeWorker{started: make(chan struct{})}
	rt := testServeRuntime(t, fc, fw)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := fw.started
	done := make(chan error, 1)
	go func() { done <- rt.Serve(ctx) }()
	var err error
	select {
	case err = <-done:
	case <-started:
		t.Fatal("worker started before unsupported version was rejected")
	}
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Code != core.CodeUnsupported {
		t.Fatalf("want unsupported, got %v", err)
	}
	if pe.Details["server_version"] != "1.33.0" || pe.Details["supported"] != SupportedServerVersion {
		t.Fatalf("details: %#v", pe.Details)
	}
	if fc.ServeOps()[0] != "get_system_info" {
		t.Fatalf("ops %v", fc.ServeOps())
	}
	if len(fc.ServeOps()) != 1 {
		t.Fatalf("expected no search attributes, got %v", fc.ServeOps())
	}
	_, _, _, _, starts, _ := fw.snapshot()
	if starts != 0 {
		t.Fatal("worker started")
	}
	if fc.CloseCount() != 1 {
		t.Fatalf("close %d", fc.CloseCount())
	}
}

func TestServeNoHost(t *testing.T) {
	fc := &fakeClient{}
	rt, err := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var workerNew int32
	rt.dial = func(client.Options) (client.Client, error) {
		fc.recordDial(client.Options{})
		cancel()
		return fc, nil
	}
	rt.newWorker = func(client.Client, string, worker.Options) worker.Worker {
		atomic.StoreInt32(&workerNew, 1)
		return &fakeWorker{}
	}
	err = rt.Serve(ctx)
	if core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("expected unavailable without host, got %v", err)
	}
	if fc.dialCount() != 0 || len(fc.ServeOps()) != 0 {
		t.Fatalf("expected no dial or server ops before host check, dials=%d ops=%v", fc.dialCount(), fc.ServeOps())
	}
	if atomic.LoadInt32(&workerNew) != 0 {
		t.Fatal("worker created without host")
	}
	if fc.CloseCount() != 0 {
		t.Fatalf("no-host failure must not close runtime, close count %d", fc.CloseCount())
	}
}

func TestServeNoHostThenRegisterAndServe(t *testing.T) {
	fc := &fakeClient{}
	fw := &fakeWorker{started: make(chan struct{})}
	rt, err := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	if err != nil {
		t.Fatal(err)
	}
	ctxFirst, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	rt.dial = func(opts client.Options) (client.Client, error) {
		fc.recordDial(opts)
		cancelFirst()
		return fc, nil
	}
	rt.newWorker = func(_ client.Client, queue string, opts worker.Options) worker.Worker {
		fw.mu.Lock()
		fw.queue = queue
		fw.opts = opts
		fw.mu.Unlock()
		return fw
	}
	if err := rt.Serve(ctxFirst); core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("first Serve: %v", err)
	}
	_ = rt.RegisterFlowHost(func(runtime.DurableContext, runtime.FlowRef, json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	started := fw.started
	done := make(chan error, 1)
	go func() { done <- rt.Serve(ctx) }()
	waitWorkerStarted(t, started, done)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("second Serve: %v", err)
	}
}

func TestServeDialFailure(t *testing.T) {
	fc := &fakeClient{}
	fw := &fakeWorker{}
	rt, err := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	if err != nil {
		t.Fatal(err)
	}
	_ = rt.RegisterFlowHost(func(runtime.DurableContext, runtime.FlowRef, json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	})
	rt.dial = func(client.Options) (client.Client, error) {
		fc.recordDial(client.Options{})
		return nil, errors.New("dial failed")
	}
	rt.newWorker = func(client.Client, string, worker.Options) worker.Worker { return fw }
	err = rt.Serve(context.Background())
	if core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("got %v", err)
	}
	if len(fc.ServeOps()) != 0 {
		t.Fatalf("search attrs touched: %v", fc.ServeOps())
	}
	_, _, _, _, starts, _ := fw.snapshot()
	if starts != 0 {
		t.Fatal("worker created")
	}
	if fc.CloseCount() != 0 {
		t.Fatalf("dial failed before client open, close count %d", fc.CloseCount())
	}
	ctxTenant := core.WithTenant(context.Background(), "acme")
	if _, err := rt.Start(ctxTenant, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}}); core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("after dial fail Serve: %v", err)
	}
}

func TestServeSecondCallConflict(t *testing.T) {
	fc := &fakeClient{}
	fc.describeResp = &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		},
	}
	fw := &fakeWorker{started: make(chan struct{})}
	rt := testServeRuntime(t, fc, fw)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	started := fw.started
	done := make(chan error, 1)
	go func() {
		defer wg.Done()
		done <- rt.Serve(ctx)
	}()
	waitWorkerStarted(t, started, done)
	ctxCancelled, cancelSecond := context.WithCancel(context.Background())
	cancelSecond()
	err := rt.Serve(ctxCancelled)
	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("concurrent second Serve with cancelled ctx: %v", err)
	}
	if fc.CloseCount() != 0 {
		t.Fatalf("first Serve closed client early: close count %d", fc.CloseCount())
	}
	if _, _, _, _, _, stops := fw.snapshot(); stops != 0 {
		t.Fatalf("worker stopped early: %d", stops)
	}
	ctxTenant := core.WithTenant(context.Background(), "acme")
	if _, err := rt.Describe(ctxTenant, runtime.RunRef{RunID: "run_01234567890123456789012345678901"}); err != nil {
		t.Fatalf("Describe while first Serve runs: %v", err)
	}
	cancel()
	wg.Wait()
	if err := rt.Serve(context.Background()); core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("sequential third Serve: %v", err)
	}
}

func TestServeAfterShutdownUnavailable(t *testing.T) {
	fc := &fakeClient{}
	rt := testServeRuntime(t, fc, &fakeWorker{})
	rt.shutdown()
	if err := rt.Serve(context.Background()); core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("got %v", err)
	}
}

func TestServeContextCancelledBeforeStart(t *testing.T) {
	fc := &fakeClient{}
	fw := &fakeWorker{}
	rt := testServeRuntime(t, fc, fw)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := rt.Serve(ctx); err != nil {
		t.Fatalf("got %v want nil", err)
	}
	if fc.CloseCount() != 0 {
		t.Fatalf("close %d want 0 (dial skipped)", fc.CloseCount())
	}
	_, _, _, _, starts, _ := fw.snapshot()
	if starts != 0 {
		t.Fatal("worker created")
	}
}

func TestServeContextCancelledDuringVersionCheck(t *testing.T) {
	block := make(chan struct{})
	entered := make(chan struct{}, 1)
	fc := &fakeClient{}
	fc.wfSvc = &fakeWorkflowService{parent: fc, version: "1.32.0", block: block, entered: entered}
	fw := &fakeWorker{}
	rt := testServeRuntime(t, fc, fw)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rt.Serve(ctx) }()
	<-entered
	cancel()
	close(block)
	if err := <-done; err != nil {
		t.Fatalf("cancel during version check: got %v want nil shutdown", err)
	}
	if fc.CloseCount() != 1 {
		t.Fatalf("close %d", fc.CloseCount())
	}
	_, _, _, _, starts, _ := fw.snapshot()
	if starts != 0 {
		t.Fatal("worker started")
	}
}

func TestServeWorkerStartError(t *testing.T) {
	fc := &fakeClient{}
	ctx, cancel := context.WithCancel(context.Background())
	startCause := "START_FAIL_XYZZY"
	fw := &fakeWorker{
		started:  make(chan struct{}),
		startErr: errors.New(startCause),
		onStart:  cancel,
	}
	rt := testServeRuntime(t, fc, fw)
	err := rt.Serve(ctx)
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Code != core.CodeUnavailable {
		t.Fatalf("got %v", err)
	}
	if pe.Message != "temporal: cannot start the worker" {
		t.Fatalf("message: %q", pe.Message)
	}
	if strings.Contains(pe.Message, startCause) {
		t.Fatal("cause leaked into message")
	}
	for _, v := range pe.Details {
		if s, ok := v.(string); ok && strings.Contains(s, startCause) {
			t.Fatal("cause leaked into details")
		}
	}
	if fc.CloseCount() != 1 {
		t.Fatalf("close %d", fc.CloseCount())
	}
}

type serveLogRecorder struct {
	mu    sync.Mutex
	msgs  []string
	attrs []map[string]string
}

func (h *serveLogRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (h *serveLogRecorder) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	if r.Message == "temporal: serving" {
		m := make(map[string]string)
		r.Attrs(func(a slog.Attr) bool {
			m[a.Key] = a.Value.String()
			return true
		})
		h.attrs = append(h.attrs, m)
	}
	return nil
}

func (h *serveLogRecorder) WithAttrs(attrs []slog.Attr) slog.Handler { return h }

func (h *serveLogRecorder) WithGroup(string) slog.Handler { return h }

func (h *serveLogRecorder) snapshot() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.msgs))
	copy(out, h.msgs)
	return out
}

func TestServeStartupShutdownLogs(t *testing.T) {
	rec := &serveLogRecorder{}
	fc := &fakeClient{}
	fw := &fakeWorker{started: make(chan struct{})}
	rt, err := New(Options{
		Payloads: testSecretsCodec(t),
		Blobs:    testBlobStore(t),
		Outbox:   newTestOutbox(),
		Logger:   slog.New(rec),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = rt.RegisterFlowHost(func(runtime.DurableContext, runtime.FlowRef, json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	})
	rt.dial = func(opts client.Options) (client.Client, error) {
		fc.recordDial(opts)
		return fc, nil
	}
	fw.mu.Lock()
	if fw.started == nil {
		fw.started = make(chan struct{})
	}
	fw.mu.Unlock()
	rt.newWorker = func(_ client.Client, queue string, opts worker.Options) worker.Worker {
		fw.mu.Lock()
		fw.queue = queue
		fw.opts = opts
		fw.mu.Unlock()
		return fw
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := fw.started
	done := make(chan error, 1)
	go func() { done <- rt.Serve(ctx) }()
	waitWorkerStarted(t, started, done)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	assertServingLogAttrs(t, rec, DefaultTaskQueue, client.DefaultNamespace)
	msgs := rec.snapshot()
	if !containsInOrder(msgs, "temporal: serving", "temporal: stopped") {
		t.Fatalf("log order: %v", msgs)
	}
	_, _, _, _, _, stops := fw.snapshot()
	if stops != 1 {
		t.Fatalf("stop count: %d", stops)
	}
}

func TestServeStartupShutdownLogsCustomNamespace(t *testing.T) {
	const customNS = "acme-prod"
	rec := &serveLogRecorder{}
	fc := &fakeClient{}
	fw := &fakeWorker{started: make(chan struct{})}
	rt, err := New(Options{
		Payloads: testSecretsCodec(t),
		Blobs:    testBlobStore(t),
		Outbox:   newTestOutbox(),
		Logger:   slog.New(rec),
		Client:   client.Options{Namespace: customNS},
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = rt.RegisterFlowHost(func(runtime.DurableContext, runtime.FlowRef, json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	})
	rt.dial = func(opts client.Options) (client.Client, error) {
		fc.recordDial(opts)
		return fc, nil
	}
	rt.newWorker = func(_ client.Client, queue string, opts worker.Options) worker.Worker {
		fw.mu.Lock()
		fw.queue = queue
		fw.opts = opts
		fw.mu.Unlock()
		return fw
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := fw.started
	done := make(chan error, 1)
	go func() { done <- rt.Serve(ctx) }()
	waitWorkerStarted(t, started, done)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	assertServingLogAttrs(t, rec, DefaultTaskQueue, customNS)
	msgs := rec.snapshot()
	if !containsInOrder(msgs, "temporal: serving", "temporal: stopped") {
		t.Fatalf("log order: %v", msgs)
	}
}

func TestServeFatalShutdownLogs(t *testing.T) {
	rec := &serveLogRecorder{}
	fc := &fakeClient{}
	fw := &fakeWorker{started: make(chan struct{})}
	rt, err := New(Options{
		Payloads: testSecretsCodec(t),
		Blobs:    testBlobStore(t),
		Outbox:   newTestOutbox(),
		Logger:   slog.New(rec),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = rt.RegisterFlowHost(func(runtime.DurableContext, runtime.FlowRef, json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	})
	rt.dial = func(opts client.Options) (client.Client, error) {
		fc.recordDial(opts)
		return fc, nil
	}
	fw.mu.Lock()
	if fw.started == nil {
		fw.started = make(chan struct{})
	}
	fw.mu.Unlock()
	rt.newWorker = func(_ client.Client, queue string, opts worker.Options) worker.Worker {
		fw.mu.Lock()
		fw.opts = opts
		fw.mu.Unlock()
		return fw
	}
	started := fw.started
	done := make(chan error, 1)
	go func() { done <- rt.Serve(context.Background()) }()
	waitWorkerStarted(t, started, done)
	_, opts, _, _, _, _ := fw.snapshot()
	opts.OnFatalError(errors.New("fatal"))
	if err := <-done; core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("got %v", err)
	}
	assertServingLogAttrs(t, rec, DefaultTaskQueue, client.DefaultNamespace)
	msgs := rec.snapshot()
	if !containsInOrder(msgs, "temporal: serving", "temporal: server call failed", "temporal: stopped") {
		t.Fatalf("log order: %v", msgs)
	}
}

func assertServingLogAttrs(t *testing.T, rec *serveLogRecorder, wantQueue, wantNS string) {
	t.Helper()
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.attrs) != 1 {
		t.Fatalf("serving log count: %d", len(rec.attrs))
	}
	a := rec.attrs[0]
	if len(a) != 2 {
		t.Fatalf("serving attr count: %d attrs %#v", len(a), a)
	}
	if a["task_queue"] != wantQueue {
		t.Fatalf("task_queue: got %q want %q", a["task_queue"], wantQueue)
	}
	if a["namespace"] != wantNS {
		t.Fatalf("namespace: got %q want %q", a["namespace"], wantNS)
	}
}

func containsInOrder(msgs []string, want ...string) bool {
	j := 0
	for _, m := range msgs {
		if j < len(want) && m == want[j] {
			j++
		}
	}
	return j == len(want)
}

func TestServeCancelledBetweenStartupSteps(t *testing.T) {
	t.Run("after-dial", func(t *testing.T) {
		fc := &fakeClient{}
		fw := &fakeWorker{}
		rt := testServeRuntime(t, fc, fw)
		ctx, cancel := context.WithCancel(context.Background())
		var workerNew int32
		rt.dial = func(opts client.Options) (client.Client, error) {
			fc.recordDial(opts)
			cancel()
			return fc, nil
		}
		rt.newWorker = func(client.Client, string, worker.Options) worker.Worker {
			atomic.StoreInt32(&workerNew, 1)
			return fw
		}
		if err := rt.Serve(ctx); err != nil {
			t.Fatalf("got %v want nil", err)
		}
		if atomic.LoadInt32(&workerNew) != 0 {
			t.Fatal("worker created")
		}
		if len(fc.ServeOps()) != 0 {
			t.Fatalf("ops after dial cancel: %v", fc.ServeOps())
		}
		if fc.CloseCount() != 1 {
			t.Fatalf("close %d", fc.CloseCount())
		}
	})
	t.Run("after-get-system-info", func(t *testing.T) {
		fc := &fakeClient{}
		ctx, cancel := context.WithCancel(context.Background())
		fc.wfSvc = &fakeWorkflowService{
			parent: fc, version: "1.32.0",
			onSuccessHook: func(context.Context) { cancel() },
		}
		fw := &fakeWorker{}
		rt := testServeRuntime(t, fc, fw)
		var workerNew int32
		rt.newWorker = func(client.Client, string, worker.Options) worker.Worker {
			atomic.StoreInt32(&workerNew, 1)
			return fw
		}
		if err := rt.Serve(ctx); err != nil {
			t.Fatalf("got %v want nil", err)
		}
		if atomic.LoadInt32(&workerNew) != 0 {
			t.Fatal("worker created")
		}
		if fc.ServeOps()[0] != "get_system_info" || len(fc.ServeOps()) != 1 {
			t.Fatalf("ops: %v", fc.ServeOps())
		}
		if fc.CloseCount() != 1 {
			t.Fatalf("close %d", fc.CloseCount())
		}
	})
	t.Run("after-list-search-attributes", func(t *testing.T) {
		fc := &fakeClient{}
		ctx, cancel := context.WithCancel(context.Background())
		fc.opSvc = &fakeOperatorService{
			parent: fc,
			custom: map[string]enumspb.IndexedValueType{
				FlowSearchAttribute: enumspb.INDEXED_VALUE_TYPE_KEYWORD,
			},
			onListSuccessHook: func(context.Context) { cancel() },
		}
		fw := &fakeWorker{}
		rt := testServeRuntime(t, fc, fw)
		var workerNew int32
		rt.newWorker = func(client.Client, string, worker.Options) worker.Worker {
			atomic.StoreInt32(&workerNew, 1)
			return fw
		}
		if err := rt.Serve(ctx); err != nil {
			t.Fatalf("got %v want nil", err)
		}
		if atomic.LoadInt32(&workerNew) != 0 {
			t.Fatal("worker created")
		}
		wantOps := []string{"get_system_info", "list_search_attributes"}
		got := fc.ServeOps()
		if len(got) != len(wantOps) {
			t.Fatalf("ops: %v", got)
		}
		for i, w := range wantOps {
			if got[i] != w {
				t.Fatalf("op[%d]: got %q want %q", i, got[i], w)
			}
		}
		if fc.CloseCount() != 1 {
			t.Fatalf("close %d", fc.CloseCount())
		}
	})
	t.Run("after-add-search-attributes", func(t *testing.T) {
		fc := &fakeClient{}
		ctx, cancel := context.WithCancel(context.Background())
		fc.opSvc = &fakeOperatorService{
			parent:           fc,
			onAddSuccessHook: func(context.Context) { cancel() },
		}
		fw := &fakeWorker{}
		rt := testServeRuntime(t, fc, fw)
		var workerNew int32
		rt.newWorker = func(client.Client, string, worker.Options) worker.Worker {
			atomic.StoreInt32(&workerNew, 1)
			return fw
		}
		if err := rt.Serve(ctx); err != nil {
			t.Fatalf("got %v want nil", err)
		}
		if atomic.LoadInt32(&workerNew) != 0 {
			t.Fatal("worker created")
		}
		if fc.operatorAddCalls() != 1 {
			t.Fatalf("add calls: %d", fc.operatorAddCalls())
		}
		if len(fc.ServeOps()) != 3 {
			t.Fatalf("ops: %v", fc.ServeOps())
		}
		if fc.CloseCount() != 1 {
			t.Fatalf("close %d", fc.CloseCount())
		}
	})
}

func TestServeWorkerFatalError(t *testing.T) {
	fc := &fakeClient{}
	fw := &fakeWorker{started: make(chan struct{})}
	rt := testServeRuntime(t, fc, fw)
	started := fw.started
	done := make(chan error, 1)
	go func() { done <- rt.Serve(context.Background()) }()
	waitWorkerStarted(t, started, done)
	_, opts, _, _, _, _ := fw.snapshot()
	if opts.OnFatalError == nil {
		t.Fatal("OnFatalError not configured")
	}
	fatalCause := "namespace not found XYZZY"
	opts.OnFatalError(errors.New(fatalCause))
	err := <-done
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Code != core.CodeUnavailable {
		t.Fatalf("code: %v", err)
	}
	wantMsg := "temporal: the worker stopped after a fatal error"
	if pe.Message != wantMsg {
		t.Fatalf("message: %q", pe.Message)
	}
	if strings.Contains(pe.Message, fatalCause) {
		t.Fatal("cause leaked into message")
	}
	if pe.Details["op"] != "worker" {
		t.Fatalf("details: %#v", pe.Details)
	}
	for _, v := range pe.Details {
		if s, ok := v.(string); ok && strings.Contains(s, fatalCause) {
			t.Fatal("cause leaked into details")
		}
	}
	_, _, _, _, _, stops := fw.snapshot()
	if stops != 1 {
		t.Fatalf("stop count: %d", stops)
	}
	if fc.CloseCount() != 1 {
		t.Fatalf("close %d", fc.CloseCount())
	}
	ctxTenant := core.WithTenant(context.Background(), "acme")
	if _, err := rt.Start(ctxTenant, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}}); core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("after fatal Serve: %v", err)
	}
}

func TestServeUnsupportedVersionDetailTruncation(t *testing.T) {
	long := "1.33.0" + strings.Repeat("é", 100)
	fc := &fakeClient{}
	fc.wfSvc = &fakeWorkflowService{parent: fc, version: long}
	rt := testServeRuntime(t, fc, &fakeWorker{})
	err := rt.Serve(context.Background())
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Code != core.CodeUnsupported {
		t.Fatalf("got %v", err)
	}
	detail, _ := pe.Details["server_version"].(string)
	if utf8.RuneCountInString(detail) != 64 {
		t.Fatalf("detail runes: %d", utf8.RuneCountInString(detail))
	}
	if !utf8.ValidString(detail) {
		t.Fatal("detail not valid UTF-8")
	}
	exact64 := strings.Repeat("z", 64)
	fc2 := &fakeClient{}
	fc2.wfSvc = &fakeWorkflowService{parent: fc2, version: exact64}
	rt2 := testServeRuntime(t, fc2, &fakeWorker{})
	err2 := rt2.Serve(context.Background())
	if !errors.As(err2, &pe) {
		t.Fatalf("got %v", err2)
	}
	if pe.Details["server_version"] != exact64 {
		t.Fatalf("64-rune detail: %q", pe.Details["server_version"])
	}
}

func TestServeGetSystemInfoUnavailable(t *testing.T) {
	fc := &fakeClient{}
	fc.wfSvc = &fakeWorkflowService{parent: fc, err: &serviceerror.Unavailable{}}
	fw := &fakeWorker{}
	rt := testServeRuntime(t, fc, fw)
	err := rt.Serve(context.Background())
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Code != core.CodeUnavailable {
		t.Fatalf("got %v", err)
	}
	if pe.Details["op"] != "get_system_info" {
		t.Fatalf("details: %#v", pe.Details)
	}
	_, _, _, _, starts, _ := fw.snapshot()
	if starts != 0 {
		t.Fatal("worker started")
	}
	if fc.CloseCount() != 1 {
		t.Fatalf("close %d", fc.CloseCount())
	}
}
