package temporal

import (
	"context"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
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

func TestEmitActivityRejectsMissingOrMalformedTenant(t *testing.T) {
	ob := newRecordingOutbox()
	opts := testOptions(t)
	opts.Outbox = ob
	rt, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}

	// No tenant header: the propagator leaves the activity context without one.
	env := newActivityEnv(t, rt, "")
	if _, err := env.ExecuteActivity(rt.runEmitActivity, emitActivityInput{RunID: "run", Seq: 1, Type: "a"}); err == nil {
		t.Fatal("emit without a tenant accepted")
	} else if code := core.CodeOf(activityToCore(err)); code != core.CodeInvalidArgument {
		t.Fatalf("error code %v want invalid_argument (%v)", code, err)
	}

	// A malformed tenant is dropped by TenantFrom, same as an absent one.
	err = rt.runEmitActivity(core.WithTenant(context.Background(), "acme/eu"),
		emitActivityInput{RunID: "run", Seq: 1, Type: "a"})
	if code := core.CodeOf(activityToCore(err)); code != core.CodeInvalidArgument {
		t.Fatalf("error code %v want invalid_argument (%v)", code, err)
	}
	if calls := ob.callsCopy(); len(calls) != 0 {
		t.Fatalf("outbox appends %+v want none", calls)
	}
}
