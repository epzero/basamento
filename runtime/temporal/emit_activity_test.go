package temporal

import (
	"context"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

func activityWorkflowIDProbe(ctx context.Context) (string, error) {
	return workflowIDFromActivity(ctx), nil
}

func newActivityEnv(t *testing.T, rt *Runtime, tenant string) *testsuite.TestActivityEnvironment {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.SetContextPropagators([]workflow.ContextPropagator{TenantPropagator{}})
	p, err := converter.GetDefaultDataConverter().ToPayload(tenant)
	if err != nil {
		t.Fatal(err)
	}
	env.SetHeader(&commonpb.Header{Fields: map[string]*commonpb.Payload{tenantHeaderKey: p}})
	env.RegisterActivity(activityWorkflowIDProbe)
	env.RegisterActivity(rt.runEmitActivity)
	return env
}

func TestEmitActivityBindsRunIDToSchedulingWorkflow(t *testing.T) {
	rt, err := New(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	env := newActivityEnv(t, rt, "acme")

	var wfID string
	val, err := env.ExecuteActivity(activityWorkflowIDProbe)
	if err != nil {
		t.Fatal(err)
	}
	if err := val.Get(&wfID); err != nil {
		t.Fatal(err)
	}
	if wfID == "" {
		t.Fatal("activity has no workflow id")
	}
	if _, err := env.ExecuteActivity(rt.runEmitActivity, emitActivityInput{RunID: wfID, Seq: 1, Type: "a"}); err != nil {
		t.Fatalf("matching run id rejected: %v", err)
	}
	if _, err := env.ExecuteActivity(rt.runEmitActivity, emitActivityInput{RunID: "forged", Seq: 1, Type: "a"}); err == nil {
		t.Fatal("forged run id accepted")
	}
}

func TestEmitActivityRejectsWrongTenant(t *testing.T) {
	ob := newTestOutbox()
	ob.wantTenant = "acme"
	opts := testOptions(t)
	opts.Outbox = ob
	rt, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	env := newActivityEnv(t, rt, "beta")
	var wfID string
	val, err := env.ExecuteActivity(activityWorkflowIDProbe)
	if err != nil {
		t.Fatal(err)
	}
	if err := val.Get(&wfID); err != nil {
		t.Fatal(err)
	}
	if _, err := env.ExecuteActivity(rt.runEmitActivity, emitActivityInput{RunID: wfID, Seq: 1, Type: "a"}); err == nil {
		t.Fatal("emit for a run of another tenant accepted")
	}
}
