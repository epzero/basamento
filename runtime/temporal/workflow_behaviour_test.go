package temporal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func finishWorkflowInt(t *testing.T, env *testsuite.TestWorkflowEnvironment) int {
	t.Helper()
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out int
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	return out
}

func finishWorkflowInt64(t *testing.T, env *testsuite.TestWorkflowEnvironment) int64 {
	t.Helper()
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out int64
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	return out
}

func finishWorkflowStrings(t *testing.T, env *testsuite.TestWorkflowEnvironment) []string {
	t.Helper()
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("workflow error: %v", err)
	}
	var out []string
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatalf("workflow result: %v", err)
	}
	return out
}

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
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(idx)
		return b, nil
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "s"}, RunID: "r1"})
	if got := finishWorkflowInt(t, env); got != 1 {
		t.Fatalf("idx %d", got)
	}
}

func TestSelectReturnsFirstTimerWhenListedFirst(t *testing.T) {
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		idx, err := dc.Select(dc.Sleep(time.Second), dc.Sleep(2*time.Second))
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(idx)
		return b, nil
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "s"}, RunID: "r1"})
	if got := finishWorkflowInt(t, env); got != 0 {
		t.Fatalf("idx %d want 0", got)
	}
}

func TestSelectReturnsImmediateBeforeTimer(t *testing.T) {
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		ready := dc.Sleep(0)
		idx, err := dc.Select(ready, dc.Sleep(time.Hour))
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(idx)
		return b, nil
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "s"}, RunID: "r1"})
	if got := finishWorkflowInt(t, env); got != 0 {
		t.Fatalf("idx %d", got)
	}
}

func registerQuickStep(t *testing.T, rt *Runtime, name string) {
	t.Helper()
	if err := rt.RegisterStep(name, func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"done"`), nil
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
}

func TestSelectReturnsReadyStepBeforeLongTimer(t *testing.T) {
	rt, env := testEnv(t)
	registerQuickStep(t, rt, "now")
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		step := dc.Step("now", nil, runtime.StepOptions{})
		idx, err := dc.Select(step, dc.Sleep(time.Hour))
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(idx)
		return b, nil
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "s"}, RunID: "r1"})
	if got := finishWorkflowInt(t, env); got != 0 {
		t.Fatalf("idx %d want 0", got)
	}
}

func TestSelectReturnsReadyStepListedSecond(t *testing.T) {
	rt, env := testEnv(t)
	registerQuickStep(t, rt, "now")
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		step := dc.Step("now", nil, runtime.StepOptions{})
		idx, err := dc.Select(dc.Sleep(time.Hour), step)
		if err != nil {
			return nil, err
		}
		b, _ := json.Marshal(idx)
		return b, nil
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "s"}, RunID: "r1"})
	if got := finishWorkflowInt(t, env); got != 1 {
		t.Fatalf("idx %d want 1", got)
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
		return json.Marshal(after.Sub(before).Nanoseconds())
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "s"}, RunID: "r1"})
	if got := finishWorkflowInt64(t, env); got != int64(2*time.Second) {
		t.Fatalf("elapsed %d want %d", got, int64(2*time.Second))
	}
}

func TestRetryBackoffAdvancesMockClock(t *testing.T) {
	var n int
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		before := dc.Now()
		var raw json.RawMessage
		if err := dc.Step("charge", nil, runtime.StepOptions{MaxAttempts: 3}).Get(&raw); err != nil {
			return nil, err
		}
		after := dc.Now()
		return json.Marshal(after.Sub(before).Nanoseconds())
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
	if n != 3 {
		t.Fatalf("attempts %d", n)
	}
	if got := finishWorkflowInt64(t, env); got != int64(3*time.Second) {
		t.Fatalf("elapsed %d want %d", got, int64(3*time.Second))
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

func TestMissingTenantHeaderFailsBeforeStepNoConverterTenant(t *testing.T) {
	var n int
	rt, env := testEnv(t)
	env.SetDataConverter(rt.DataConverter())
	in, _ := json.Marshal(map[string]string{"probe": "1"})
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var raw json.RawMessage
		return raw, dc.Step("x", in, runtime.StepOptions{}).Get(&raw)
	})
	_ = rt.RegisterStep("x", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		n++
		return json.RawMessage(`null`), nil
	}, runtime.StepDefaults{})
	_ = rt.Register(env)
	var panicVal any
	func() {
		defer func() {
			panicVal = recover()
		}()
		env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "t"}, RunID: "r1"})
	}()
	if panicVal == nil {
		t.Fatalf("expected panic, wf err=%v", env.GetWorkflowError())
	}
	msg := fmt.Sprint(panicVal)
	if !strings.Contains(msg, "temporal: no tenant in context") {
		t.Fatalf("panic %q", msg)
	}
	if core.CodeOf(fmt.Errorf("%s", msg)) != core.CodeInvalidArgument && !strings.Contains(msg, string(core.CodeInvalidArgument)) {
		t.Fatalf("want invalid_argument in %q", msg)
	}
	if n != 0 {
		t.Fatalf("step runs %d", n)
	}
}

func TestMissingTenantHeaderFailsBeforeStepClientEncodedOnly(t *testing.T) {
	var n int
	rt, env := testEnv(t)
	in, _ := json.Marshal(map[string]string{"probe": "1"})
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var raw json.RawMessage
		return raw, dc.Step("x", in, runtime.StepOptions{}).Get(&raw)
	})
	_ = rt.RegisterStep("x", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		n++
		return json.RawMessage(`null`), nil
	}, runtime.StepDefaults{})
	_ = rt.Register(env)
	var panicVal any
	func() {
		defer func() {
			panicVal = recover()
		}()
		env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "t"}, RunID: "r1"})
	}()
	if panicVal != nil {
		msg := fmt.Sprint(panicVal)
		if !strings.Contains(msg, "temporal: no tenant in context") {
			t.Fatalf("panic %q", msg)
		}
	} else if wfErr := env.GetWorkflowError(); wfErr != nil {
		if !strings.Contains(wfErr.Error(), "temporal: no tenant in context") {
			t.Fatalf("workflow error %v", wfErr)
		}
	} else {
		t.Fatal("expected panic or workflow error without tenant header")
	}
	if n != 0 {
		t.Fatalf("step runs %d", n)
	}
}

func TestIdempotencyKeyOverridesDefaultActivityID(t *testing.T) {
	rt, env := testEnv(t)
	var ids []string
	env.SetOnActivityStartedListener(func(info *activity.Info, _ context.Context, _ converter.EncodedValues) {
		ids = append(ids, info.ActivityID)
	})
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
		return json.RawMessage(`"a"`), nil
	}, runtime.StepDefaults{})
	_ = rt.RegisterStep("other", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`"b"`), nil
	}, runtime.StepDefaults{})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "t"}, RunID: "r1"})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"custom-id": false, "other": false}
	for _, id := range ids {
		if _, ok := want[id]; ok {
			want[id] = true
		}
	}
	if !want["custom-id"] || !want["other"] {
		t.Fatalf("activity ids %v", ids)
	}
}

func TestUnsupportedDurableOps(t *testing.T) {
	rt, env := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var scratch any
		childErr := dc.Child(runtime.FlowRef{}, nil, runtime.ChildOptions{}).Get(&scratch)
		awaitErr := dc.Await(runtime.GateSpec{}).Get(&scratch)
		codes := []string{
			string(core.CodeOf(dc.Emit(runtime.ProgressEvent{}))),
			string(core.CodeOf(dc.Checkpoint(runtime.SnapshotRef{}))),
			string(core.CodeOf(childErr)),
			string(core.CodeOf(awaitErr)),
		}
		return json.Marshal(codes)
	})
	_ = rt.Register(env)
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "u"}, RunID: "r1"})
	got := finishWorkflowStrings(t, env)
	want := []string{"unsupported", "unsupported", "unsupported", "unsupported"}
	if len(got) != len(want) {
		t.Fatalf("codes %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("codes[%d]=%q want %q", i, got[i], want[i])
		}
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
	if !strings.Contains(err.Error(), "temporal: the flow host panicked") {
		t.Fatalf("error %v", err)
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
