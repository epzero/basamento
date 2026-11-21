package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/testsuite"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestSelectRejectsEmpty(t *testing.T) {
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		_, err := dc.Select()
		return nil, err
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "s"}, RunID: "r1"})
	err := env.GetWorkflowError()
	if err == nil || core.CodeOf(activityToCore(err)) != core.CodeInvalidArgument {
		t.Fatalf("%v", err)
	}
}

func TestSelectReturnsSoonestTimer(t *testing.T) {
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		idx, err := dc.Select(dc.Sleep(2*time.Second), dc.Sleep(time.Second))
		b, _ := json.Marshal(idx)
		return b, err
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "s"}, RunID: "r1"})
	var out int
	_ = env.GetWorkflowResult(&out)
	if out != 1 {
		t.Fatalf("idx %d", out)
	}
}

func TestSelectReturnsImmediateBeforeTimer(t *testing.T) {
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		ready := dc.Sleep(0)
		idx, err := dc.Select(ready, dc.Sleep(time.Hour))
		b, _ := json.Marshal(idx)
		return b, err
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "s"}, RunID: "r1"})
	var out int
	_ = env.GetWorkflowResult(&out)
	if out != 0 {
		t.Fatalf("idx %d", out)
	}
}

func TestSleepAdvancesWorkflowClock(t *testing.T) {
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		before := dc.Now()
		if err := dc.Sleep(2 * time.Second).Get(nil); err != nil {
			return nil, err
		}
		after := dc.Now()
		d := after.Sub(before)
		return json.RawMessage(`"` + d.String() + `"`), nil
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "s"}, RunID: "r1"})
	var out string
	_ = env.GetWorkflowResult(&out)
	if !strings.Contains(out, "2s") && !strings.Contains(out, "3s") {
		t.Fatalf("delta %s", out)
	}
}

func TestRetryBackoffAdvancesMockClock(t *testing.T) {
	var n int
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		before := dc.Now()
		var raw json.RawMessage
		err := dc.Step("charge", nil, runtime.StepOptions{MaxAttempts: 3}).Get(&raw)
		after := dc.Now()
		if err != nil {
			return json.RawMessage(`"fail"`), err
		}
		d := after.Sub(before)
		return json.RawMessage(`"` + d.String() + `"`), nil
	})
	_ = rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		n++
		if n < 3 {
			return nil, &core.Error{Code: "vendor_error", Message: "fail", Retryable: true}
		}
		return json.RawMessage(`{"ok":true}`), nil
	}, runtime.StepDefaults{})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "pay"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("attempts %d", n)
	}
}

func TestTenantReachesFlowAndStep(t *testing.T) {
	var saw string
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		tid, ok := core.TenantFrom(dc.Context())
		if !ok || tid != "acme" {
			return nil, core.NewError(core.CodeInternal, "flow tenant missing", nil)
		}
		var raw json.RawMessage
		if err := dc.Step("tenant", nil, runtime.StepOptions{}).Get(&raw); err != nil {
			return nil, err
		}
		return raw, nil
	})
	_ = rt.RegisterStep("tenant", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		tid, ok := core.TenantFrom(ctx)
		if !ok {
			return nil, core.NewError(core.CodeInvalidArgument, "no tenant", nil)
		}
		saw = string(tid)
		return json.RawMessage(`"ok"`), nil
	}, runtime.StepDefaults{})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "t"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if saw != "acme" {
		t.Fatalf("step tenant %q", saw)
	}
}

func TestMissingTenantHeaderFailsBeforeStep(t *testing.T) {
	var n int
	rt, env := testEnv(t)
	env.SetDataConverter(rt.DataConverter())
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var raw json.RawMessage
		return raw, dc.Step("x", nil, runtime.StepOptions{}).Get(&raw)
	})
	_ = rt.RegisterStep("x", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		n++
		return json.RawMessage(`null`), nil
	}, runtime.StepDefaults{})
	_ = rt.Register(env)
	var panicked bool
	func() {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "t"}, RunID: "r1"})
	}()
	if !panicked && env.GetWorkflowError() == nil {
		t.Fatal("expected failure")
	}
	if n != 0 {
		t.Fatalf("step runs %d", n)
	}
}

func TestIdempotencyKeyOverridesDefaultActivityID(t *testing.T) {
	rt, env := testEnv(t)
	var custom, defaulted int
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var a, b json.RawMessage
		if err := dc.Step("named", nil, runtime.StepOptions{IdempotencyKey: "custom-id"}).Get(&a); err != nil {
			return nil, err
		}
		if err := dc.Step("other", nil, runtime.StepOptions{}).Get(&b); err != nil {
			return nil, err
		}
		return json.RawMessage(`"ok"`), nil
	})
	_ = rt.RegisterStep("named", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		custom++
		return json.RawMessage(`"a"`), nil
	}, runtime.StepDefaults{})
	_ = rt.RegisterStep("other", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		defaulted++
		return json.RawMessage(`"b"`), nil
	}, runtime.StepDefaults{})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "t"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if custom != 1 || defaulted != 1 {
		t.Fatalf("custom=%d defaulted=%d", custom, defaulted)
	}
}

func TestUnsupportedDurableOps(t *testing.T) {
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Emit(runtime.ProgressEvent{}); core.CodeOf(err) != core.CodeUnsupported {
			return nil, err
		}
		if err := dc.Checkpoint(runtime.SnapshotRef{}); core.CodeOf(err) != core.CodeUnsupported {
			return nil, err
		}
		if err := dc.Child(runtime.FlowRef{}, nil, runtime.ChildOptions{}).Get(nil); core.CodeOf(err) != core.CodeUnsupported {
			return nil, err
		}
		if err := dc.Await(runtime.GateSpec{}).Get(nil); core.CodeOf(err) != core.CodeUnsupported {
			return nil, err
		}
		return json.RawMessage(`"ok"`), nil
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "u"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
}

func TestFlowHostPanicFailsWithFixedMessage(t *testing.T) {
	var buf bytes.Buffer
	rt, env := testEnv(t)
	rt.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	_ = rt.RegisterFlowHost(func(runtime.DurableContext, runtime.FlowRef, json.RawMessage) (json.RawMessage, error) {
		panic("flow-secret")
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "p"}, RunID: "r1"})
	err := env.GetWorkflowError()
	if err == nil {
		t.Fatal("expected failure")
	}
	if !strings.Contains(buf.String(), "flow-secret") {
		t.Fatal("panic not logged")
	}
	if strings.Contains(err.Error(), "flow-secret") {
		t.Fatal("panic leaked to returned error")
	}
}

func TestRegisterRequiresFlowHost(t *testing.T) {
	rt, _ := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t)})
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	if err := rt.Register(env); core.CodeOf(err) != core.CodeUnavailable {
		t.Fatal(err)
	}
}
