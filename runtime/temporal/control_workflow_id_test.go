package temporal

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestDescribeSendsTenantPrefixedWorkflowID(t *testing.T) {
	start := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	fc := &fakeClient{describeResp: describeResp(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, start, start)}
	rt := testControlRuntime(t, fc)
	ctx := core.WithTenant(context.Background(), "acme")
	if _, err := rt.Describe(ctx, runtime.RunRef{RunID: "r1"}); err != nil {
		t.Fatalf("Describe failed: %v", err)
	}
	call := mustLastCall(fc, "describe")
	if call.DescribeID != "acme/r1" {
		t.Fatalf("Describe workflow id: got %q want %q", call.DescribeID, "acme/r1")
	}
}

func TestCancelSendsTenantPrefixedWorkflowID(t *testing.T) {
	fc := &fakeClient{}
	rt := testControlRuntime(t, fc)
	ctx := core.WithTenant(context.Background(), "acme")
	if err := rt.Cancel(ctx, runtime.RunRef{RunID: "r1"}, "why"); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	call := mustLastCall(fc, "cancel")
	if call.CancelID != "acme/r1" {
		t.Fatalf("Cancel workflow id: got %q want %q", call.CancelID, "acme/r1")
	}
	if call.CancelRunID != "" {
		t.Fatalf("Cancel run id: got %q want empty", call.CancelRunID)
	}
}

func TestResolveSendsTenantPrefixedWorkflowID(t *testing.T) {
	fc := &fakeClient{updateHandle: fakeUpdateHandle{}}
	rt := testControlRuntime(t, fc)
	ctx := core.WithTenant(context.Background(), "acme")
	payload := json.RawMessage(`{}`)
	if err := rt.Resolve(ctx, runtime.RunRef{RunID: "r1"}, "gate1", payload); err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	call := mustLastCall(fc, "update")
	if call.UpdateOpts.WorkflowID != "acme/r1" {
		t.Fatalf("Resolve workflow id: got %q want %q", call.UpdateOpts.WorkflowID, "acme/r1")
	}
	if call.UpdateOpts.WaitForStage != client.WorkflowUpdateStageCompleted {
		t.Fatalf("WaitForStage: got %v want Completed", call.UpdateOpts.WaitForStage)
	}
}
