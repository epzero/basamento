package steps

import (
	"errors"
	"strings"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
)

func TestTemporalStub_FinishCloseTime(t *testing.T) {
	stub, clk := newStubForTest(t)
	c := dialStub(t, stub)
	mustStart(t, c, client.StartWorkflowOptions{ID: "fin-t", TaskQueue: "q"})
	desc, err := c.WorkflowService().DescribeWorkflowExecution(t.Context(), &workflowservice.DescribeWorkflowExecutionRequest{
		Namespace: stubNamespace,
		Execution: &commonpb.WorkflowExecution{WorkflowId: "fin-t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if desc.WorkflowExecutionInfo.CloseTime != nil {
		t.Fatal("running")
	}
	clk.Add(10 * time.Second)
	closeAt := clk.Now()
	if err := stub.Finish("fin-t", enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED); err != nil {
		t.Fatal(err)
	}
	desc2, err := c.WorkflowService().DescribeWorkflowExecution(t.Context(), &workflowservice.DescribeWorkflowExecutionRequest{
		Namespace: stubNamespace,
		Execution: &commonpb.WorkflowExecution{WorkflowId: "fin-t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !desc2.WorkflowExecutionInfo.CloseTime.AsTime().Equal(closeAt) {
		t.Fatal("close time")
	}
	if desc2.WorkflowExecutionInfo.StartTime.AsTime().Equal(closeAt) {
		t.Fatal("close differs from start")
	}
}

func TestTemporalStub_FinishUnknownOrClosed(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	mustStart(t, c, client.StartWorkflowOptions{ID: "fc", TaskQueue: "q"})
	if err := stub.Finish("missing", enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED); err == nil {
		t.Fatal("unknown")
	}
	if err := stub.Finish("fc", enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED); err != nil {
		t.Fatal(err)
	}
	err := stub.Finish("fc", enumspb.WORKFLOW_EXECUTION_STATUS_FAILED)
	if err == nil {
		t.Fatal("already closed")
	}
	var nf *serviceerror.NotFound
	if !errors.As(err, &nf) {
		t.Fatalf("type %T", err)
	}
}

func TestTemporalStub_DescribeLatestExecution(t *testing.T) {
	stub, clk := newStubForTest(t)
	c := dialStub(t, stub)
	id := "dup-W"
	r1 := mustStart(t, c, client.StartWorkflowOptions{ID: id, TaskQueue: "q"})
	if err := stub.Finish(id, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED); err != nil {
		t.Fatal(err)
	}
	clk.Add(time.Second)
	opts := client.StartWorkflowOptions{
		ID: id, TaskQueue: "q",
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	}
	r2 := mustStart(t, c, opts)
	wantStart := stub.Starts()[len(stub.Starts())-1].StartTime
	latest, err := c.DescribeWorkflowExecution(t.Context(), id, "")
	if err != nil {
		t.Fatal(err)
	}
	if latest.WorkflowExecutionInfo.GetExecution().GetRunId() != r2.GetRunID() {
		t.Fatal("latest run")
	}
	if !latest.WorkflowExecutionInfo.StartTime.AsTime().Equal(wantStart) {
		t.Fatal("start time")
	}
	old, err := c.DescribeWorkflowExecution(t.Context(), id, r1.GetRunID())
	if err != nil {
		t.Fatal(err)
	}
	if old.WorkflowExecutionInfo.Status != enumspb.WORKFLOW_EXECUTION_STATUS_FAILED {
		t.Fatal("old failed")
	}
}

func TestTemporalStub_DescribeNotFound(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	mustStart(t, c, client.StartWorkflowOptions{ID: "known", TaskQueue: "q"})
	_, err := c.DescribeWorkflowExecution(t.Context(), "missing", "")
	if err == nil {
		t.Fatal("unknown wf")
	}
	_, err = c.DescribeWorkflowExecution(t.Context(), "known", "00000000-0000-0000-0000-000099999999")
	if err == nil {
		t.Fatal("unknown run")
	}
	var nf *serviceerror.NotFound
	if !errors.As(err, &nf) {
		t.Fatalf("type %T", err)
	}
}

func TestTemporalStub_CancelAndUpdate(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	mustStart(t, c, client.StartWorkflowOptions{ID: "dcu", TaskQueue: "q"})
	_, err := c.UpdateWorkflow(t.Context(), client.UpdateWorkflowOptions{
		WorkflowID: "dcu", UpdateName: "sync", WaitForStage: client.WorkflowUpdateStageCompleted,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.Updates()) != 1 || stub.Updates()[0].Name != "sync" || stub.Updates()[0].WorkflowID != "dcu" {
		t.Fatalf("updates %#v", stub.Updates())
	}
	if err := c.CancelWorkflow(t.Context(), "dcu", ""); err != nil {
		t.Fatal(err)
	}
	if !stub.Executions()[0].CancelRequested {
		t.Fatal("cancel flag")
	}
	if err := stub.Finish("dcu", enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED); err != nil {
		t.Fatal(err)
	}
	err = c.CancelWorkflow(t.Context(), "dcu", "")
	if err == nil || !strings.Contains(err.Error(), "already completed") {
		t.Fatalf("cancel closed %v", err)
	}
	_, err = c.UpdateWorkflow(t.Context(), client.UpdateWorkflowOptions{
		WorkflowID: "dcu", UpdateName: "late", WaitForStage: client.WorkflowUpdateStageCompleted,
	})
	if err == nil {
		t.Fatal("update closed")
	}
	var nf *serviceerror.NotFound
	if !errors.As(err, &nf) {
		t.Fatalf("update closed type %T", err)
	}
	if err.Error() != "workflow execution already completed" {
		t.Fatalf("update closed message %q", err.Error())
	}
	_, err = c.UpdateWorkflow(t.Context(), client.UpdateWorkflowOptions{
		WorkflowID: "missing", UpdateName: "x", WaitForStage: client.WorkflowUpdateStageCompleted,
	})
	if err == nil {
		t.Fatal("update unknown")
	}
	if !errors.As(err, &nf) {
		t.Fatalf("update unknown type %T", err)
	}
	if err.Error() != "workflow not found" {
		t.Fatalf("update unknown message %q", err.Error())
	}
}
