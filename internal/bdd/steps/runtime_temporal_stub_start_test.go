package steps

import (
	"errors"
	"fmt"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
)

func TestTemporalStub_StartPolicyMatrix(t *testing.T) {
	type outcome struct {
		errTyp string
	}
	cases := []struct {
		name     string
		wfID     string
		setup    func(stub *temporalStub, c client.Client, id string)
		policy   enumspb.WorkflowIdReusePolicy
		conflict enumspb.WorkflowIdConflictPolicy
		want     outcome
	}{
		{name: "running_fail", wfID: "m1", setup: func(_ *temporalStub, c client.Client, id string) {
			mustStart(t, c, client.StartWorkflowOptions{ID: id, TaskQueue: "q"})
		}, conflict: enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL, want: outcome{errTyp: "*serviceerror.WorkflowExecutionAlreadyStarted"}},
		{name: "running_use_existing", wfID: "m2", setup: func(_ *temporalStub, c client.Client, id string) {
			mustStart(t, c, client.StartWorkflowOptions{ID: id, TaskQueue: "q"})
		}, conflict: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING},
		{name: "closed_reject", wfID: "m3", setup: func(stub *temporalStub, c client.Client, id string) {
			mustStart(t, c, client.StartWorkflowOptions{ID: id, TaskQueue: "q"})
			if err := stub.Finish(id, enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED); err != nil {
				t.Fatal(err)
			}
		}, policy: enumspb.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE, want: outcome{errTyp: "*serviceerror.WorkflowExecutionAlreadyStarted"}},
		{name: "closed_allow", wfID: "m4", setup: func(stub *temporalStub, c client.Client, id string) {
			mustStart(t, c, client.StartWorkflowOptions{ID: id, TaskQueue: "q"})
			if err := stub.Finish(id, enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED); err != nil {
				t.Fatal(err)
			}
		}, policy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE},
		{name: "failed_only_after_fail", wfID: "m5", setup: func(stub *temporalStub, c client.Client, id string) {
			mustStart(t, c, client.StartWorkflowOptions{ID: id, TaskQueue: "q"})
			if err := stub.Finish(id, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED); err != nil {
				t.Fatal(err)
			}
		}, policy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY},
		{name: "failed_only_after_completed", wfID: "m6", setup: func(stub *temporalStub, c client.Client, id string) {
			mustStart(t, c, client.StartWorkflowOptions{ID: id, TaskQueue: "q"})
			if err := stub.Finish(id, enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED); err != nil {
				t.Fatal(err)
			}
		}, policy: enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY, want: outcome{errTyp: "*serviceerror.WorkflowExecutionAlreadyStarted"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub, _ := newStubForTest(t)
			c := dialStub(t, stub)
			tc.setup(stub, c, tc.wfID)
			before := len(stub.Starts())
			opts := client.StartWorkflowOptions{
				ID: tc.wfID, TaskQueue: "q",
				WorkflowIDReusePolicy: tc.policy, WorkflowIDConflictPolicy: tc.conflict,
				WorkflowExecutionErrorWhenAlreadyStarted: true,
			}
			_, err := c.ExecuteWorkflow(t.Context(), opts, noopWorkflow)
			if tc.want.errTyp != "" {
				if err == nil || fmt.Sprintf("%T", err) != tc.want.errTyp {
					t.Fatalf("err %v type %T", err, err)
				}
				if len(stub.Starts()) != before {
					t.Fatal("rejected start must not record")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.conflict == enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING && len(stub.Starts()) != before {
				t.Fatal("USE_EXISTING must not add start")
			}
		})
	}
}

func TestTemporalStub_StartRefusedPolicies(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	_, err := c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{
		ID: "term", TaskQueue: "q",
		WorkflowIDConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_TERMINATE_EXISTING,
	}, noopWorkflow)
	if err == nil {
		t.Fatal("terminate existing")
	}
	var ia *serviceerror.InvalidArgument
	if !errors.As(err, &ia) {
		t.Fatalf("terminate existing type %T", err)
	}
	_, err = c.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{
		ID: "term2", TaskQueue: "q",
		WorkflowIDReusePolicy: enumspb.WORKFLOW_ID_REUSE_POLICY_TERMINATE_IF_RUNNING, //nolint:staticcheck
	}, noopWorkflow)
	if err == nil {
		t.Fatal("terminate if running")
	}
	if !errors.As(err, &ia) {
		t.Fatalf("terminate if running type %T", err)
	}
	if len(stub.Starts()) != 0 || len(stub.Executions()) != 0 {
		t.Fatal("refused starts must leave no trace")
	}
}

func TestTemporalStub_RejectedStartNoTrace(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	opts := client.StartWorkflowOptions{
		ID: "wf-1", TaskQueue: "q",
		WorkflowIDConflictPolicy:                 enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}
	mustStart(t, c, opts)
	before := len(stub.Starts())
	_, err := c.ExecuteWorkflow(t.Context(), opts, noopWorkflow)
	if err == nil {
		t.Fatal("expected conflict")
	}
	if len(stub.Starts()) != before || len(stub.Executions()) != 1 {
		t.Fatal("rejected start trace")
	}
	_, err = c.WorkflowService().StartWorkflowExecution(t.Context(), &workflowservice.StartWorkflowExecutionRequest{
		Namespace: stubNamespace, WorkflowId: "wf-1",
		WorkflowType:             &commonpb.WorkflowType{Name: "T"},
		TaskQueue:                &taskqueuepb.TaskQueue{Name: "q"},
		WorkflowIdConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
	})
	if err == nil {
		t.Fatal("expected already started")
	}
	if len(stub.Starts()) != before {
		t.Fatal("raw RPC rejected start trace")
	}
}

func TestTemporalStub_SearchAttributesOnStart(t *testing.T) {
	stub, _ := newStubForTest(t)
	stub.PreRegisterSearchAttribute("BasamentoFlow", enumspb.INDEXED_VALUE_TYPE_KEYWORD)
	c := dialStub(t, stub)
	mustStart(t, c, client.StartWorkflowOptions{
		ID: "wf-sa", TaskQueue: "q",
		TypedSearchAttributes: temporal.NewSearchAttributes(
			temporal.NewSearchAttributeKeyKeyword("BasamentoFlow").ValueSet("pay"),
		),
	})
	if stub.Starts()[0].SearchAttributes["BasamentoFlow"] != "pay" {
		t.Fatalf("sa %#v", stub.Starts()[0].SearchAttributes)
	}
	stub2, _ := newStubForTest(t)
	c2 := dialStub(t, stub2)
	before := len(stub2.Starts())
	_, err := c2.ExecuteWorkflow(t.Context(), client.StartWorkflowOptions{
		ID: "bad", TaskQueue: "q",
		TypedSearchAttributes: temporal.NewSearchAttributes(
			temporal.NewSearchAttributeKeyKeyword("UnknownAttr").ValueSet("x"),
		),
	}, noopWorkflow)
	if err == nil {
		t.Fatal("unregistered attr")
	}
	if len(stub2.Starts()) != before {
		t.Fatal("rejected")
	}
}

func TestTemporalStub_DeterministicRunIDs(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	r1 := mustStart(t, c, client.StartWorkflowOptions{ID: "rid-1", TaskQueue: "q"})
	if r1.GetRunID() != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("run1 %s", r1.GetRunID())
	}
	opts := client.StartWorkflowOptions{
		ID: "rid-1", TaskQueue: "q",
		WorkflowIDConflictPolicy:                 enumspb.WORKFLOW_ID_CONFLICT_POLICY_FAIL,
		WorkflowExecutionErrorWhenAlreadyStarted: true,
	}
	_, err := c.ExecuteWorkflow(t.Context(), opts, noopWorkflow)
	if err == nil {
		t.Fatal("conflict expected")
	}
	r2 := mustStart(t, c, client.StartWorkflowOptions{ID: "rid-2", TaskQueue: "q"})
	if r2.GetRunID() != "00000000-0000-0000-0000-000000000002" {
		t.Fatalf("run2 %s", r2.GetRunID())
	}
}

func TestTemporalStub_StartTimeMatchesClock(t *testing.T) {
	stub, clk := newStubForTest(t)
	c := dialStub(t, stub)
	clk.Add(5 * time.Second)
	want := clk.Now()
	mustStart(t, c, client.StartWorkflowOptions{ID: "clock-wf", TaskQueue: "q"})
	st := stub.Starts()[0]
	if !st.StartTime.Equal(want) {
		t.Fatalf("start %v want %v", st.StartTime, want)
	}
	resp, err := c.WorkflowService().DescribeWorkflowExecution(t.Context(), &workflowservice.DescribeWorkflowExecutionRequest{
		Namespace: stubNamespace,
		Execution: &commonpb.WorkflowExecution{WorkflowId: "clock-wf", RunId: st.RunID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.WorkflowExecutionInfo.StartTime.AsTime().Equal(want) {
		t.Fatal("describe start time")
	}
}

func TestTemporalStub_UseExistingOnClosedStartsNew(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	id := "use-closed"
	mustStart(t, c, client.StartWorkflowOptions{ID: id, TaskQueue: "q"})
	if err := stub.Finish(id, enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED); err != nil {
		t.Fatal(err)
	}
	before := len(stub.Starts())
	_, err := c.WorkflowService().StartWorkflowExecution(t.Context(), &workflowservice.StartWorkflowExecutionRequest{
		Namespace: stubNamespace, WorkflowId: id,
		WorkflowType:             &commonpb.WorkflowType{Name: "T"},
		TaskQueue:                &taskqueuepb.TaskQueue{Name: "q"},
		WorkflowIdConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
		WorkflowIdReusePolicy:    enumspb.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.Starts()) != before+1 {
		t.Fatal("USE_EXISTING on closed must start new run")
	}
}

func TestTemporalStub_UseExistingRunningStartedFalse(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	id := "use-run"
	mustStart(t, c, client.StartWorkflowOptions{ID: id, TaskQueue: "q"})
	before := len(stub.Starts())
	resp, err := c.WorkflowService().StartWorkflowExecution(t.Context(), &workflowservice.StartWorkflowExecutionRequest{
		Namespace: stubNamespace, WorkflowId: id,
		WorkflowType:             &commonpb.WorkflowType{Name: "T"},
		TaskQueue:                &taskqueuepb.TaskQueue{Name: "q"},
		WorkflowIdConflictPolicy: enumspb.WORKFLOW_ID_CONFLICT_POLICY_USE_EXISTING,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetStarted() {
		t.Fatal("USE_EXISTING must return Started=false")
	}
	if len(stub.Starts()) != before {
		t.Fatal("no new start record")
	}
}
