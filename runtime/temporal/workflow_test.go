package temporal

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/runtime"
)

func testEnv(t *testing.T) (*Runtime, *testsuite.TestWorkflowEnvironment, converter.DataConverter) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(time.Hour)
	codec, err := NewCodec(newTestSecrets(t), newTestBlob(t), DefaultClaimCheckBytes)
	if err != nil {
		t.Fatal(err)
	}
	blobs := newTestBlob(t)
	rt, err := New(Options{Codec: codec, Blobs: blobs})
	if err != nil {
		t.Fatal(err)
	}
	rawDC, err := rt.BuildDataConverter(nil)
	if err != nil {
		t.Fatal(err)
	}
	typed, ok := rawDC.(*DataConverter)
	if !ok {
		t.Fatal("data converter type")
	}
	dc := typed.WithFallbackTenant("acme")
	env.SetDataConverter(dc)
	env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: time.Hour})
	env.SetContextPropagators([]workflow.ContextPropagator{TenantPropagator{}})
	return rt, env, dc
}

func setTestTenantHeader(env *testsuite.TestWorkflowEnvironment, tenant string) {
	p, _ := converter.GetDefaultDataConverter().ToPayload(tenant)
	env.SetHeader(&commonpb.Header{Fields: map[string]*commonpb.Payload{tenantHeaderKey: p}})
}

func TestNewIDAndRandAreDeterministicOnReplay(t *testing.T) {
	rt, env, dc := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		first := dc.NewID("span")
		second := dc.NewID("span")
		if first == second {
			return nil, invalidErr("consecutive ids are equal", nil)
		}
		r1 := dc.Rand("k")
		r2 := dc.Rand("k")
		if r1 != r2 {
			return nil, invalidErr("Rand(key) did not return the same source", nil)
		}
		return json.RawMessage(`"` + first + `"`), nil
	})
	if err := rt.RegisterWorkflowsActivities(env); err != nil {
		t.Fatal(err)
	}
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{
		Flow: runtime.FlowRef{Name: "h"}, Tenant: "acme", RunID: "r1",
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var out json.RawMessage
	if err := env.GetWorkflowResult(&out); err != nil {
		t.Fatal(err)
	}
	if string(out) != `"span_1"` {
		t.Fatalf("id %s, want span_1", out)
	}
	var suite2 testsuite.WorkflowTestSuite
	env2 := suite2.NewTestWorkflowEnvironment()
	env2.SetDataConverter(dc)
	env2.SetTestTimeout(time.Hour)
	env2.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: time.Hour})
	env2.SetContextPropagators([]workflow.ContextPropagator{TenantPropagator{}})
	if err := rt.RegisterWorkflowsActivities(env2); err != nil {
		t.Fatal(err)
	}
	setTestTenantHeader(env2, "acme")
	env2.ExecuteWorkflow(HostWorkflowName, HostInput{
		Flow: runtime.FlowRef{Name: "h"}, Tenant: "acme", RunID: "r2",
	})
	if err := env2.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	var out2 json.RawMessage
	if err := env2.GetWorkflowResult(&out2); err != nil {
		t.Fatal(err)
	}
	if string(out) != string(out2) {
		t.Fatalf("ids differ across runs: %s vs %s", out, out2)
	}
}

func TestStepPanicFailsRun(t *testing.T) {
	rt, env, _ := testEnv(t)
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		var raw json.RawMessage
		if err := dc.Step("panic", nil, runtime.StepOptions{MaxAttempts: 1}).Get(&raw); err != nil {
			return nil, err
		}
		return raw, nil
	})
	_ = rt.RegisterStep("panic", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		panic("secret")
	}, runtime.StepDefaults{})
	if err := rt.RegisterWorkflowsActivities(env); err != nil {
		t.Fatal(err)
	}
	setTestTenantHeader(env, "acme")
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "p"}, Tenant: "acme", RunID: "r1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatal("expected workflow failure")
	}
}
