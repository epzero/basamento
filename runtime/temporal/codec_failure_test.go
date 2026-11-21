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

	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/runtime"
)

type faultBlob struct {
	inner    platform.Blob
	mu       sync.Mutex
	putErr   *core.Error
	getErr   *core.Error
	getArm   bool
	putCount int
}

func (b *faultBlob) Put(ctx context.Context, key string, r io.Reader, contentType string) error {
	b.mu.Lock()
	b.putCount++
	err := b.putErr
	b.mu.Unlock()
	if err != nil {
		return err
	}
	return b.inner.Put(ctx, key, r, contentType)
}

func (b *faultBlob) setPutErr(err *core.Error) {
	b.mu.Lock()
	b.putErr = err
	b.mu.Unlock()
}

func (b *faultBlob) putCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.putCount
}

func (b *faultBlob) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	armed := b.getArm
	err := b.getErr
	b.mu.Unlock()
	if armed && err != nil {
		return nil, err
	}
	return b.inner.Get(ctx, key)
}

func (b *faultBlob) Exists(ctx context.Context, key string) (bool, error) {
	return b.inner.Exists(ctx, key)
}

func (b *faultBlob) Delete(ctx context.Context, key string) error {
	return b.inner.Delete(ctx, key)
}

func (b *faultBlob) SignedURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return b.inner.SignedURL(ctx, key, ttl)
}

func (b *faultBlob) StartUpload(ctx context.Context, o platform.UploadOptions) (platform.Upload, error) {
	return b.inner.StartUpload(ctx, o)
}

func (b *faultBlob) Commit(ctx context.Context, stagingKey string) (string, error) {
	return b.inner.Commit(ctx, stagingKey)
}

func (b *faultBlob) armGet() {
	b.mu.Lock()
	b.getArm = true
	b.mu.Unlock()
}

func largeStepInput(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(bytes.Repeat([]byte("x"), 6000))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func largeStepResult() json.RawMessage {
	raw, _ := json.Marshal(bytes.Repeat([]byte("y"), 6000))
	return raw
}

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testEnvWithBlobs(t *testing.T, blobs platform.Blob, logger *slog.Logger) (*Runtime, *testsuite.TestWorkflowEnvironment) {
	t.Helper()
	rt, err := New(Options{Payloads: testSecretsCodec(t), Blobs: blobs, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(time.Hour)
	env.SetDataConverter(&clientConverter{inner: rt.DataConverter(), tenant: "acme"})
	env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: time.Hour})
	env.SetContextPropagators([]workflow.ContextPropagator{TenantPropagator{}})
	return rt, env
}

func TestCodecTransientPutFailsWorkflowTask(t *testing.T) {
	cases := []struct {
		name string
		err  *core.Error
	}{
		{"unavailable", core.NewError(core.CodeUnavailable, "SECRET-BLOB-OUTAGE", nil)},
		{"timeout", core.NewError(core.CodeTimeout, "SECRET-TIMEOUT", nil)},
		{"rate_limited", core.NewError(core.CodeRateLimited, "SECRET-RATE", nil)},
		{"retryable_custom", &core.Error{Code: "vendor_error", Message: "SECRET-RETRY", Retryable: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var runs int
			var buf bytes.Buffer
			fb := &faultBlob{inner: testBlobStore(t), putErr: tc.err}
			rt, env := testEnvWithBlobs(t, fb, testLogger(&buf))
			in := largeStepInput(t)
			_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
				var out json.RawMessage
				return out, dc.Step("big", in, runtime.StepOptions{}).Get(&out)
			})
			_ = rt.RegisterStep("big", func(context.Context, json.RawMessage) (json.RawMessage, error) {
				runs++
				return json.RawMessage(`"ok"`), nil
			}, runtime.StepDefaults{})
			_ = rt.Register(env)
			setTestTenantHeader(env, "acme")
			env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"})
			wfErr := env.GetWorkflowError()
			var panicErr *sdktemporal.PanicError
			if !errors.As(wfErr, &panicErr) {
				t.Fatalf("workflow error %T %v", wfErr, wfErr)
			}
			msg := panicErr.Error()
			if !strings.Contains(msg, string(tc.err.Code)) {
				t.Fatalf("panic message %q", msg)
			}
			if strings.Contains(msg, "SECRET") {
				t.Fatalf("leaked cause in panic %q", msg)
			}
			if runs != 0 {
				t.Fatalf("step runs %d", runs)
			}
			if !strings.Contains(buf.String(), "SECRET") {
				t.Fatalf("log %q", buf.String())
			}
		})
	}
}

func TestCodecPermanentPutFailsStepFuture(t *testing.T) {
	var runs int
	fb := &faultBlob{
		inner:  testBlobStore(t),
		putErr: core.NewError(core.CodePermissionDenied, "SECRET-DENIED", nil),
	}
	rt, env := testEnvWithBlobs(t, fb, nil)
	in := largeStepInput(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var a, b json.RawMessage
		e1 := dc.Step("big", in, runtime.StepOptions{}).Get(&a)
		e2 := dc.Step("big", in, runtime.StepOptions{}).Get(&b)
		return json.Marshal([]string{string(core.CodeOf(e1)), string(core.CodeOf(e2))})
	})
	_ = rt.RegisterStep("big", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		runs++
		return json.RawMessage(`"ok"`), nil
	}, runtime.StepDefaults{})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var codes []string
	if err := env.GetWorkflowResult(&codes); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("step runs %d", runs)
	}
	if codes[0] != string(core.CodePermissionDenied) || codes[1] != string(core.CodePermissionDenied) {
		t.Fatalf("codes %v", codes)
	}
	if fb.putCalls() != 1 {
		t.Fatalf("put calls %d want 1", fb.putCalls())
	}
}

func TestCodecTransientGetFailsWorkflowTask(t *testing.T) {
	var runs int
	fb := &faultBlob{inner: testBlobStore(t)}
	rt, env := testEnvWithBlobs(t, fb, nil)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		return out, dc.Step("echo", nil, runtime.StepOptions{}).Get(&out)
	})
	_ = rt.RegisterStep("echo", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		runs++
		fb.getErr = core.NewError(core.CodeUnavailable, "SECRET-READ", nil)
		fb.armGet()
		return largeStepResult(), nil
	}, runtime.StepDefaults{})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"})
	var panicErr *sdktemporal.PanicError
	if !errors.As(env.GetWorkflowError(), &panicErr) {
		t.Fatalf("wf err %v", env.GetWorkflowError())
	}
	if !strings.Contains(panicErr.Error(), "unavailable") || strings.Contains(panicErr.Error(), "SECRET") {
		t.Fatalf("%q", panicErr.Error())
	}
	if runs != 1 {
		t.Fatalf("runs %d", runs)
	}
}

func TestCodecCancelledPutFailsWorkflowTask(t *testing.T) {
	var runs int
	fb := &faultBlob{
		inner:  testBlobStore(t),
		putErr: core.NewError(core.CodeCancelled, "SECRET-PUT-SLOW", nil),
	}
	rt, env := testEnvWithBlobs(t, fb, nil)
	in := largeStepInput(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		return out, dc.Step("big", in, runtime.StepOptions{}).Get(&out)
	})
	_ = rt.RegisterStep("big", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		runs++
		return json.RawMessage(`"ok"`), nil
	}, runtime.StepDefaults{})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"})
	var panicErr *sdktemporal.PanicError
	if !errors.As(env.GetWorkflowError(), &panicErr) {
		t.Fatalf("workflow error %T %v", env.GetWorkflowError(), env.GetWorkflowError())
	}
	if !strings.Contains(panicErr.Error(), "cancelled") || strings.Contains(panicErr.Error(), "SECRET") {
		t.Fatalf("panic message %q", panicErr.Error())
	}
	if runs != 0 {
		t.Fatalf("step runs %d", runs)
	}
}

func TestCodecCancelledGetFailsWorkflowTask(t *testing.T) {
	var runs int
	fb := &faultBlob{inner: testBlobStore(t)}
	rt, env := testEnvWithBlobs(t, fb, nil)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		return out, dc.Step("echo", nil, runtime.StepOptions{}).Get(&out)
	})
	_ = rt.RegisterStep("echo", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		runs++
		fb.getErr = core.NewError(core.CodeCancelled, "SECRET-GET-SLOW", nil)
		fb.armGet()
		return largeStepResult(), nil
	}, runtime.StepDefaults{})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"})
	var panicErr *sdktemporal.PanicError
	if !errors.As(env.GetWorkflowError(), &panicErr) {
		t.Fatalf("wf err %v", env.GetWorkflowError())
	}
	if !strings.Contains(panicErr.Error(), "cancelled") || strings.Contains(panicErr.Error(), "SECRET") {
		t.Fatalf("%q", panicErr.Error())
	}
	if runs != 1 {
		t.Fatalf("runs %d", runs)
	}
}

func TestCodecPermanentInvalidArgumentPutFailsFlowWithCode(t *testing.T) {
	fb := &faultBlob{
		inner:  testBlobStore(t),
		putErr: core.NewError(core.CodeInvalidArgument, "bad blob", nil),
	}
	rt, env := testEnvWithBlobs(t, fb, nil)
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
	if env.GetWorkflowError() == nil {
		t.Fatal("expected failure")
	}
	if core.CodeOf(activityToCore(env.GetWorkflowError())) != core.CodeInvalidArgument {
		t.Fatalf("code %v", env.GetWorkflowError())
	}
}

func TestCodecPermanentGetFailsStep(t *testing.T) {
	fb := &faultBlob{inner: testBlobStore(t)}
	rt, env := testEnvWithBlobs(t, fb, nil)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		return out, dc.Step("echo", nil, runtime.StepOptions{}).Get(&out)
	})
	_ = rt.RegisterStep("echo", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		fb.getErr = core.NewError(core.CodeNotFound, "blob missing", nil)
		fb.armGet()
		return largeStepResult(), nil
	}, runtime.StepDefaults{})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow failure")
	}
	if core.CodeOf(activityToCore(env.GetWorkflowError())) != core.CodeNotFound {
		t.Fatalf("code %v", env.GetWorkflowError())
	}
}
