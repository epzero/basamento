package steps

import (
	"errors"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	operatorservice "go.temporal.io/api/operatorservice/v1"
	"go.temporal.io/api/serviceerror"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestTemporalStub_WrongNamespaceRefused(t *testing.T) {
	stub, _ := newStubForTest(t)
	c := dialStub(t, stub)
	mustStart(t, c, client.StartWorkflowOptions{ID: "ns-wf", TaskQueue: "q"})
	if err := stub.Finish("ns-wf", enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED); err != nil {
		t.Fatal(err)
	}
	other := "other-ns"
	type rpcCase struct {
		name string
		call func() error
	}
	cases := []rpcCase{
		{name: "DescribeNamespace", call: func() error {
			_, err := c.WorkflowService().DescribeNamespace(t.Context(), &workflowservice.DescribeNamespaceRequest{Namespace: other})
			return err
		}},
		{name: "StartWorkflowExecution", call: func() error {
			_, err := c.WorkflowService().StartWorkflowExecution(t.Context(), &workflowservice.StartWorkflowExecutionRequest{
				Namespace: other, WorkflowId: "x",
				WorkflowType: &commonpb.WorkflowType{Name: "T"},
				TaskQueue:    &taskqueuepb.TaskQueue{Name: "q"},
			})
			return err
		}},
		{name: "DescribeWorkflowExecution", call: func() error {
			_, err := c.WorkflowService().DescribeWorkflowExecution(t.Context(), &workflowservice.DescribeWorkflowExecutionRequest{
				Namespace: other, Execution: &commonpb.WorkflowExecution{WorkflowId: "ns-wf"},
			})
			return err
		}},
		{name: "RequestCancelWorkflowExecution", call: func() error {
			_, err := c.WorkflowService().RequestCancelWorkflowExecution(t.Context(), &workflowservice.RequestCancelWorkflowExecutionRequest{
				Namespace: other, WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: "ns-wf"},
			})
			return err
		}},
		{name: "UpdateWorkflowExecution", call: func() error {
			_, err := c.WorkflowService().UpdateWorkflowExecution(t.Context(), &workflowservice.UpdateWorkflowExecutionRequest{
				Namespace: other, WorkflowExecution: &commonpb.WorkflowExecution{WorkflowId: "ns-wf"},
			})
			return err
		}},
		{name: "ListWorkflowExecutions", call: func() error {
			_, err := c.WorkflowService().ListWorkflowExecutions(t.Context(), &workflowservice.ListWorkflowExecutionsRequest{
				Namespace: other, Query: `WorkflowId STARTS_WITH "ns-"`,
			})
			return err
		}},
		{name: "ListSearchAttributes", call: func() error {
			_, err := c.OperatorService().ListSearchAttributes(t.Context(), &operatorservice.ListSearchAttributesRequest{Namespace: other})
			return err
		}},
		{name: "AddSearchAttributes", call: func() error {
			_, err := c.OperatorService().AddSearchAttributes(t.Context(), &operatorservice.AddSearchAttributesRequest{
				Namespace: other,
				SearchAttributes: map[string]enumspb.IndexedValueType{
					"X": enumspb.INDEXED_VALUE_TYPE_KEYWORD,
				},
			})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			startsBefore := len(stub.Starts())
			updBefore := len(stub.Updates())
			cancelBefore := stub.Executions()[0].CancelRequested
			err := tc.call()
			if err == nil {
				t.Fatal("expected error")
			}
			var nf *serviceerror.NamespaceNotFound
			if !errors.As(err, &nf) {
				t.Fatalf("type %T", err)
			}
			st, _ := status.FromError(err)
			if st.Code() != codes.NotFound && st.Code() != codes.Unknown {
				t.Fatalf("code %v", st.Code())
			}
			if len(stub.Starts()) != startsBefore || len(stub.Updates()) != updBefore {
				t.Fatal("side effect")
			}
			if stub.Executions()[0].CancelRequested != cancelBefore {
				t.Fatal("cancel flag")
			}
		})
	}
}
