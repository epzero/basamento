package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	stderrors "errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	sdktemporal "go.temporal.io/sdk/temporal"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// Rand golden values for run "r1", key "k". Must equal runtime/local seededRand; change both or neither.
const (
	randR1K0 = uint64(2528446594469947107)
	randR1K1 = uint64(3139284286379284610)
	randR1K2 = uint64(4187653836303174209)
)

func testEnv(t *testing.T) (*Runtime, *testsuite.TestWorkflowEnvironment) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(time.Hour)
	rt, err := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t)})
	if err != nil {
		t.Fatal(err)
	}
	env.SetDataConverter(&clientConverter{inner: rt.DataConverter(), tenant: "acme"})
	env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: time.Hour})
	env.SetContextPropagators([]workflow.ContextPropagator{TenantPropagator{}})
	return rt, env
}

func setTestTenantHeader(env *testsuite.TestWorkflowEnvironment, tenant string) {
	p, _ := converter.GetDefaultDataConverter().ToPayload(tenant)
	env.SetHeader(&commonpb.Header{Fields: map[string]*commonpb.Payload{tenantHeaderKey: p}})
}

func TestNewIDIncrementsPerKindWithoutSideEffect(t *testing.T) {
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		a := dc.NewID("span")
		b := dc.NewID("span")
		c := dc.NewID("other")
		return json.Marshal([]string{a, b, c})
	})
	if err := rt.Register(env); err != nil {
		t.Fatal(err)
	}
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "h"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var out []string
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatal(err)
	}
	want := []string{"span_1", "span_2", "other_1"}
	for i, w := range want {
		if out[i] != w {
			t.Fatalf("ids[%d]=%q want %q", i, out[i], w)
		}
	}
}

func TestRandMatchesLocalGoldenValues(t *testing.T) {
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		r := dc.Rand("k")
		return json.Marshal([]uint64{r.Uint64(), r.Uint64(), r.Uint64()})
	})
	if err := rt.Register(env); err != nil {
		t.Fatal(err)
	}
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "h"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var got []uint64
	_ = env.GetWorkflowResult(&got)
	want := []uint64{randR1K0, randR1K1, randR1K2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Rand Uint64[%d]=%d want %d", i, got[i], want[i])
		}
	}
}

func TestStepPanicFailsOnceWithoutLeakingValue(t *testing.T) {
	var buf bytes.Buffer
	var n int
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	rt, env := testEnv(t)
	rt.logger = logger
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var raw json.RawMessage
		return raw, dc.Step("panic", nil, runtime.StepOptions{}).Get(&raw)
	})
	_ = rt.RegisterStep("panic", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		n++
		panic("secret-leak")
	}, runtime.StepDefaults{})
	if err := rt.Register(env); err != nil {
		t.Fatal(err)
	}
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "p"}, RunID: "r1"})
	wfErr := env.GetWorkflowError()
	if wfErr == nil {
		t.Fatal("expected workflow failure")
	}
	if n != 1 {
		t.Fatalf("step ran %d times", n)
	}
	if got := buf.String(); !bytes.Contains([]byte(got), []byte("secret-leak")) {
		t.Fatal("panic value not logged")
	}
	if strings.Contains(wfErr.Error(), "secret-leak") {
		t.Fatal("panic value leaked to workflow error")
	}
}

func TestStepPlainErrorDoesNotLeakToFutureGet(t *testing.T) {
	var buf bytes.Buffer
	var n int
	rt, env := testEnv(t)
	rt.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var raw json.RawMessage
		err := dc.Step("bad", nil, runtime.StepOptions{}).Get(&raw)
		return raw, err
	})
	_ = rt.RegisterStep("bad", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		n++
		return nil, stderrors.New("secret-token")
	}, runtime.StepDefaults{})
	if err := rt.Register(env); err != nil {
		t.Fatal(err)
	}
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "p"}, RunID: "r1"})
	wfErr := env.GetWorkflowError()
	if wfErr == nil {
		t.Fatal("expected failure")
	}
	if n != 1 {
		t.Fatalf("step ran %d times", n)
	}
	if !strings.Contains(buf.String(), "secret-token") {
		t.Fatal("expected secret in log")
	}
	errText := wfErr.Error()
	var app *sdktemporal.ApplicationError
	if stderrors.As(wfErr, &app) {
		errText = app.Message()
	}
	if strings.Contains(errText, "secret-token") {
		t.Fatal("secret in workflow error")
	}
	if strings.Contains(failureChainText(wfErr), "secret-token") {
		t.Fatal("secret in failure chain")
	}
}

func TestRetryableFailureRunsMaxAttempts(t *testing.T) {
	var n int
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var raw json.RawMessage
		err := dc.Step("charge", nil, runtime.StepOptions{MaxAttempts: 3}).Get(&raw)
		return raw, err
	})
	_ = rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		n++
		return nil, &core.Error{Code: "vendor_error", Message: "fail", Retryable: true}
	}, runtime.StepDefaults{})
	if err := rt.Register(env); err != nil {
		t.Fatal(err)
	}
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "pay"}, RunID: "r1"})
	_ = env.GetWorkflowError()
	if n != 3 {
		t.Fatalf("executions %d, want 3", n)
	}
}

func TestNonRetryableFailureRunsOnce(t *testing.T) {
	var n int
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var raw json.RawMessage
		return raw, dc.Step("charge", nil, runtime.StepOptions{MaxAttempts: 3}).Get(&raw)
	})
	_ = rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		n++
		return nil, &core.Error{Code: "invalid_argument", Message: "no", Retryable: false}
	}, runtime.StepDefaults{})
	if err := rt.Register(env); err != nil {
		t.Fatal(err)
	}
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "pay"}, RunID: "r1"})
	_ = env.GetWorkflowError()
	if n != 1 {
		t.Fatalf("executions %d, want 1", n)
	}
}

func TestStepMemoizedByName(t *testing.T) {
	var n int
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		f := dc.Step("once", nil, runtime.StepOptions{})
		var a, b json.RawMessage
		if err := f.Get(&a); err != nil {
			return nil, err
		}
		if err := dc.Step("once", nil, runtime.StepOptions{}).Get(&b); err != nil {
			return nil, err
		}
		if string(a) != string(b) {
			return nil, core.NewError(core.CodeInternal, "results differ", nil)
		}
		return a, nil
	})
	_ = rt.RegisterStep("once", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		n++
		return json.RawMessage(`{"n":1}`), nil
	}, runtime.StepDefaults{})
	if err := rt.Register(env); err != nil {
		t.Fatal(err)
	}
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "m"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("step ran %d times", n)
	}
}

func TestSleepZeroDurationCompletesImmediately(t *testing.T) {
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		if !dc.Sleep(0).Ready() {
			return nil, core.NewError(core.CodeInternal, "not ready", nil)
		}
		return json.RawMessage(`"ok"`), dc.Sleep(0).Get(nil)
	})
	if err := rt.Register(env); err != nil {
		t.Fatal(err)
	}
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "s"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilitiesReportOnlyWhatExists(t *testing.T) {
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		c := dc.Capabilities()
		if !c.Durable || !c.PolyglotQueues || c.SyncGateUpdates || c.ContinueAsNew {
			return nil, core.NewError(core.CodeInternal, "wrong caps", nil)
		}
		return json.RawMessage(`"ok"`), nil
	})
	if err := rt.Register(env); err != nil {
		t.Fatal(err)
	}
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
}

func TestNewRejectsNilDependencies(t *testing.T) {
	if _, err := New(Options{Blobs: testBlobStore(t)}); err == nil {
		t.Fatal("expected error without codec")
	}
	if _, err := New(Options{Payloads: testSecretsCodec(t)}); err == nil {
		t.Fatal("expected error without blobs")
	}
}
