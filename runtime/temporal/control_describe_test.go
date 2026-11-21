package temporal

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestDescribeStatusMapping(t *testing.T) {
	start := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	closeT := start.Add(time.Hour)
	table := []struct {
		st     enumspb.WorkflowExecutionStatus
		want   string
		closed bool
		bad    bool
	}{
		{enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, "running", false, false},
		{enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW, "running", false, false},
		{enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, "completed", true, false},
		{enumspb.WORKFLOW_EXECUTION_STATUS_FAILED, "failed", true, false},
		{enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT, "failed", true, false},
		{enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, "cancelled", true, false},
		{enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED, "terminated", true, false},
		{enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED, "", false, true},
		{enumspb.WORKFLOW_EXECUTION_STATUS_PAUSED, "", false, true},
	}
	for _, row := range table {
		t.Run(row.st.String(), func(t *testing.T) {
			fc := &fakeClient{describeResp: describeResp(row.st, start, closeT)}
			rt := testControlRuntime(t, fc)
			ctx := core.WithTenant(context.Background(), "acme")
			st, err := rt.Describe(ctx, runtime.RunRef{RunID: "r1"})
			if row.bad {
				var pe *core.Error
				if !errors.As(err, &pe) || pe.Code != core.CodeInternal {
					t.Fatalf("err %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if st.Ref.TenantID != "acme" || st.Ref.RunID != "r1" {
				t.Fatalf("ref %#v", st.Ref)
			}
			if st.State != row.want || !st.StartedAt.Equal(start) {
				t.Fatalf("status %#v", st)
			}
			if row.closed {
				if st.ClosedAt == nil || !st.ClosedAt.Equal(closeT) {
					t.Fatalf("closed %#v", st.ClosedAt)
				}
			} else if st.ClosedAt != nil {
				t.Fatalf("unexpected closed")
			}
		})
	}
}

func TestDescribeEmptyRunIDBeforeDial(t *testing.T) {
	var dials atomic.Int32
	fc := &fakeClient{}
	rt, err := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	if err != nil {
		t.Fatal(err)
	}
	rt.dial = func(client.Options) (client.Client, error) {
		dials.Add(1)
		return fc, nil
	}
	ctx := core.WithTenant(context.Background(), "acme")
	_, err = rt.Describe(ctx, runtime.RunRef{RunID: ""})
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err %v", err)
	}
	if len(fc.Calls()) != 0 || dials.Load() != 0 {
		t.Fatal("dialed or called client")
	}
}

func describeResp(st enumspb.WorkflowExecutionStatus, start, closeT time.Time) *workflowservice.DescribeWorkflowExecutionResponse {
	return &workflowservice.DescribeWorkflowExecutionResponse{
		WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{
			Status:    st,
			StartTime: timestamppb.New(start),
			CloseTime: timestamppb.New(closeT),
		},
	}
}
