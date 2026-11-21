package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/runtime"
)

func TestCheckpointContinuesWhenHistoryAtThreshold(t *testing.T) {
	opts := testOptions(t)
	opts.CheckpointEventThreshold = 20
	rt, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetTestTimeout(time.Hour)
	env.SetWorkerOptions(worker.Options{DeadlockDetectionTimeout: time.Hour})
	env.SetContextPropagators([]workflow.ContextPropagator{TenantPropagator{}})
	env.SetDataConverter(&clientConverter{inner: rt.DataConverter(), tenant: "acme"})
	var stepCalls int
	_ = rt.RegisterStep("s", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		stepCalls++
		return json.RawMessage(`"ok"`), nil
	}, runtime.StepDefaults{})
	_ = rt.RegisterFlowHost(func(dc runtime.DurableContext, _ runtime.FlowRef, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Step("s", nil, runtime.StepOptions{}).Get(nil); err != nil {
			return nil, err
		}
		env.SetCurrentHistoryLength(25)
		return nil, dc.Checkpoint(runtime.SnapshotRef{})
	})
	setTestTenantHeader(env, "acme")
	if err := rt.Register(env); err != nil {
		t.Fatal(err)
	}
	env.ExecuteWorkflow(HostWorkflowName, HostInput{Flow: runtime.FlowRef{Name: "c"}, RunID: "r1"})
	wfErr := env.GetWorkflowError()
	if wfErr == nil {
		t.Fatal("expected continue-as-new")
	}
	var wf *sdktemporal.WorkflowExecutionError
	if !errors.As(wfErr, &wf) {
		t.Fatalf("err %v", wfErr)
	}
	if !workflow.IsContinueAsNewError(wf.Unwrap()) {
		t.Fatalf("want continue-as-new, got %v", wfErr)
	}
	if stepCalls != 1 {
		t.Fatalf("step calls %d", stepCalls)
	}
}

func TestNewRejectsMissingOutbox(t *testing.T) {
	if _, err := New(Options{
		Payloads: testSecretsCodec(t),
		Blobs:    testBlobStore(t),
	}); err == nil {
		t.Fatal("expected error without outbox")
	}
}
