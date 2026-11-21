package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"go.temporal.io/sdk/converter"
	sdktemporal "go.temporal.io/sdk/temporal"
	sdkworkflow "go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/runtime"
)

func TestCodecFailureLogRecord(t *testing.T) {
	var buf bytes.Buffer
	codec, err := newPayloadCodec(testSecretsCodec(t), testBlobStore(t), DefaultClaimCheckBytes)
	if err != nil {
		t.Fatal(err)
	}
	dc, err := newDataConverter(codec, slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError})))
	if err != nil {
		t.Fatal(err)
	}
	d := dc.(*dataConverter)
	d.logWorkflowCodec(core.NewError(core.CodeUnavailable, "INJECTED-SECRET-CAUSE", map[string]any{"k": "v"}))
	got := buf.String()
	if !strings.Contains(got, "unavailable") || !strings.Contains(got, "INJECTED-SECRET-CAUSE") {
		t.Fatalf("log %q", got)
	}
}

func TestCodecTransientWorkflowInputDecodeFailsWorkflowTask(t *testing.T) {
	var hostRan bool
	var buf bytes.Buffer
	fb := &faultBlob{inner: testBlobStore(t)}
	rt, env := testEnvWithBlobs(t, fb, testLogger(&buf))
	in := largeStepInput(t)
	// Client-side encode stores the claim-checked blob; workflow-side decode fails.
	_ = rt.RegisterFlowHost(func(runtime.DurableContext, runtime.FlowRef, json.RawMessage) (json.RawMessage, error) {
		hostRan = true
		return nil, nil
	})
	_ = rt.Register(env)
	fb.getErr = core.NewError(core.CodeUnavailable, "SECRET-READ", nil)
	fb.armGet()
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{
		Flow: runtime.FlowRef{Name: "c"}, RunID: "r1", Input: in,
	})
	var panicErr *sdktemporal.PanicError
	if !errors.As(env.GetWorkflowError(), &panicErr) {
		t.Fatalf("wf err %T %v", env.GetWorkflowError(), env.GetWorkflowError())
	}
	if hostRan {
		t.Fatal("host ran")
	}
	msg := panicErr.Error()
	if !strings.Contains(msg, string(core.CodeUnavailable)) {
		t.Fatalf("panic message %q", msg)
	}
	if strings.Contains(msg, "SECRET") {
		t.Fatalf("leak %q", msg)
	}
	if !strings.Contains(buf.String(), "SECRET-READ") {
		t.Fatalf("log %q", buf.String())
	}
}

func TestCodecTransientWorkflowResultEncodeFailsWorkflowTask(t *testing.T) {
	var buf bytes.Buffer
	fb := &faultBlob{inner: testBlobStore(t)}
	rt, env := testEnvWithBlobs(t, fb, testLogger(&buf))
	_ = rt.RegisterFlowHost(func(_ runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		fb.setPutErr(core.NewError(core.CodeUnavailable, "SECRET-PUT", nil))
		return largeStepInput(t), nil
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"})
	var panicErr *sdktemporal.PanicError
	if !errors.As(env.GetWorkflowError(), &panicErr) {
		t.Fatalf("wf err %v", env.GetWorkflowError())
	}
	msg := panicErr.Error()
	if !strings.Contains(msg, string(core.CodeUnavailable)) {
		t.Fatalf("panic message %q", msg)
	}
	if strings.Contains(msg, "SECRET") {
		t.Fatalf("leak %q", msg)
	}
	if !strings.Contains(buf.String(), "SECRET-PUT") {
		t.Fatalf("log %q", buf.String())
	}
}

func TestCodecPermanentWorkflowInputDecodeFailsRun(t *testing.T) {
	var hostRan bool
	fb := &faultBlob{inner: testBlobStore(t)}
	rt, env := testEnvWithBlobs(t, fb, nil)
	in := largeStepInput(t)
	_ = rt.RegisterFlowHost(func(runtime.DurableContext, runtime.FlowRef, json.RawMessage) (json.RawMessage, error) {
		hostRan = true
		return nil, nil
	})
	_ = rt.Register(env)
	fb.getErr = core.NewError(core.CodeNotFound, "missing", nil)
	fb.armGet()
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{
		Flow: runtime.FlowRef{Name: "c"}, RunID: "r1", Input: in,
	})
	var panicErr *sdktemporal.PanicError
	if errors.As(env.GetWorkflowError(), &panicErr) {
		t.Fatal("unexpected panic")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected failure")
	}
	if hostRan {
		t.Fatal("host ran")
	}
}

func TestCodecPermanentWorkflowResultEncodeFailsRun(t *testing.T) {
	fb := &faultBlob{inner: testBlobStore(t)}
	rt, env := testEnvWithBlobs(t, fb, nil)
	_ = rt.RegisterFlowHost(func(_ runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		fb.setPutErr(core.NewError(core.CodePermissionDenied, "denied", nil))
		return largeStepInput(t), nil
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"})
	var panicErr *sdktemporal.PanicError
	if errors.As(env.GetWorkflowError(), &panicErr) {
		t.Fatal("unexpected panic")
	}
	if env.GetWorkflowError() == nil {
		t.Fatal("expected failure")
	}
}

func TestClientConverterReturnsTransientCodecErrors(t *testing.T) {
	fb := &faultBlob{
		inner:  testBlobStore(t),
		putErr: core.NewError(core.CodeUnavailable, "SECRET", nil),
	}
	rt, err := New(Options{Payloads: testSecretsCodec(t), Blobs: fb})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic %v", r)
		}
	}()
	aware := rt.DataConverter().(interface {
		WithContext(context.Context) converter.DataConverter
	})
	ctx := core.WithTenant(context.Background(), "acme")
	_, err = aware.WithContext(ctx).ToPayload(bytes.Repeat([]byte("x"), 6000))
	if core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("err %v", err)
	}
}

type deadlineBlob struct {
	inner      platform.Blob
	mu         sync.Mutex
	putHasDead []bool
}

func (d *deadlineBlob) Put(ctx context.Context, key string, r io.Reader, contentType string) error {
	_, has := ctx.Deadline()
	d.mu.Lock()
	d.putHasDead = append(d.putHasDead, has)
	d.mu.Unlock()
	return d.inner.Put(ctx, key, r, contentType)
}

func (d *deadlineBlob) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return d.inner.Get(ctx, key)
}
func (d *deadlineBlob) Exists(ctx context.Context, key string) (bool, error) {
	return d.inner.Exists(ctx, key)
}
func (d *deadlineBlob) Delete(ctx context.Context, key string) error { return d.inner.Delete(ctx, key) }
func (d *deadlineBlob) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return d.inner.SignedURL(ctx, key, ttl)
}
func (d *deadlineBlob) StartUpload(ctx context.Context, o platform.UploadOptions) (platform.Upload, error) {
	return d.inner.StartUpload(ctx, o)
}
func (d *deadlineBlob) Commit(ctx context.Context, stagingKey string) (string, error) {
	return d.inner.Commit(ctx, stagingKey)
}

func TestWorkflowBoundCodecIOHasDeadline(t *testing.T) {
	db := &deadlineBlob{inner: testBlobStore(t)}
	rt, env := testEnvWithBlobs(t, db, nil)
	in := largeStepInput(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		return out, dc.Step("big", in, runtime.StepOptions{}).Get(&out)
	})
	_ = rt.RegisterStep("big", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"ok"`), nil
	}, runtime.StepDefaults{})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"})
	_ = env.GetWorkflowError()
	db.mu.Lock()
	defer db.mu.Unlock()
	if len(db.putHasDead) == 0 || !db.putHasDead[0] {
		t.Fatalf("deadlines %v", db.putHasDead)
	}
}

func TestActivityToCoreMapsCanceledError(t *testing.T) {
	got := activityToCore(sdktemporal.NewCanceledError("reason"))
	if core.CodeOf(got) != core.CodeCancelled {
		t.Fatalf("code %v", core.CodeOf(got))
	}
}

type fakeWfFuture struct {
	err   error
	ready bool
}

func (f *fakeWfFuture) Get(sdkworkflow.Context, interface{}) error { return f.err }
func (f *fakeWfFuture) IsReady() bool                              { return f.ready }

func TestStepFutureGetMapsCancelThroughActivityToCore(t *testing.T) {
	sf := &stepFuture{f: &fakeWfFuture{err: sdktemporal.NewCanceledError("c"), ready: true}}
	err := sf.Get(nil)
	if core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("code %v err %v", core.CodeOf(err), err)
	}
}
