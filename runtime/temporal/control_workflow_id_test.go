package temporal

import (
	"context"
	"encoding/json"
	"strings"
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

func TestControlWorkflowIDOnAcceptedRunIDs(t *testing.T) {
	tenant := core.TenantID(strings.Repeat("t", 128))
	ctx := core.WithTenant(context.Background(), tenant)
	runIDs := []struct {
		name  string
		runID string
	}{
		{"plain", "r1"},
		{"unicode", "r\u00e9"},
		{"max_len", strings.Repeat("x", 255)},
		{"underscore", "r_1"},
		{"dot", "r.1"},
		{"spaces", "r 1"},
		{"slash_free", "a-b_c.d e"},
	}
	start := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	for _, row := range runIDs {
		t.Run(row.name, func(t *testing.T) {
			wantWF := string(tenant) + "/" + row.runID
			fc := &fakeClient{
				describeResp: describeResp(enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, start, start),
				updateHandle: fakeUpdateHandle{},
			}
			rt := testControlRuntime(t, fc)
			if _, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: row.runID}); err != nil {
				t.Fatalf("Start: %v", err)
			}
			assertCallWorkflowID(t, fc, "execute", wantWF)
			if _, err := rt.Describe(ctx, runtime.RunRef{RunID: row.runID}); err != nil {
				t.Fatalf("Describe: %v", err)
			}
			assertCallWorkflowID(t, fc, "describe", wantWF)
			if err := rt.Cancel(ctx, runtime.RunRef{RunID: row.runID}, "why"); err != nil {
				t.Fatalf("Cancel: %v", err)
			}
			assertCallWorkflowID(t, fc, "cancel", wantWF)
			if err := rt.Resolve(ctx, runtime.RunRef{RunID: row.runID}, "g", json.RawMessage(`{}`)); err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			assertCallWorkflowID(t, fc, "update", wantWF)
		})
	}
	t.Run("generated", func(t *testing.T) {
		fc := &fakeClient{}
		rt := testControlRuntime(t, fc)
		ref, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}})
		if err != nil {
			t.Fatal(err)
		}
		call := mustLastCall(fc, "execute")
		if call.ExecuteOpts.ID == "" {
			t.Fatal("empty workflow id on Start")
		}
		if call.ExecuteOpts.ID != string(tenant)+"/"+ref.RunID {
			t.Fatalf("workflow id: got %q want %q", call.ExecuteOpts.ID, string(tenant)+"/"+ref.RunID)
		}
	})
	t.Run("derived", func(t *testing.T) {
		fc := &fakeClient{}
		rt := testControlRuntime(t, fc)
		ref, err := rt.Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, IdempotencyKey: "k1"})
		if err != nil {
			t.Fatal(err)
		}
		call := mustLastCall(fc, "execute")
		if call.ExecuteOpts.ID == "" {
			t.Fatal("empty workflow id on derived Start")
		}
		if call.ExecuteOpts.ID != string(tenant)+"/"+ref.RunID {
			t.Fatalf("workflow id: got %q want %q", call.ExecuteOpts.ID, string(tenant)+"/"+ref.RunID)
		}
	})
}

func assertCallWorkflowID(t *testing.T, fc *fakeClient, op, want string) {
	t.Helper()
	call := mustLastCall(fc, op)
	var got string
	switch op {
	case "execute":
		got = call.ExecuteOpts.ID
	case "describe":
		got = call.DescribeID
	case "cancel":
		got = call.CancelID
	case "update":
		got = call.UpdateOpts.WorkflowID
	default:
		t.Fatalf("unknown op %q", op)
	}
	if got == "" {
		t.Fatalf("%s: empty workflow id", op)
	}
	if got != want {
		t.Fatalf("%s: workflow id got %q want %q", op, got, want)
	}
}
