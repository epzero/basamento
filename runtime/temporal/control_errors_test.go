package temporal

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/temporal"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestClientErrorMappedBranchesTable(t *testing.T) {
	rt, _ := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	marker := "XYZZY_SECRET_MARKER_47"
	const op = "test_op"
	cases := []struct {
		name string
		err  error
		code core.Code
		msg  string
	}{
		{"cancelled", context.Canceled, core.CodeCancelled, "temporal: the request was cancelled"},
		{"timeout", context.DeadlineExceeded, core.CodeTimeout, "temporal: the request timed out"},
		{"not_found", &serviceerror.NotFound{}, core.CodeNotFound, "temporal: run not found"},
		{"conflict", &serviceerror.WorkflowExecutionAlreadyStarted{}, core.CodeConflict, "temporal: a run with that id already exists"},
		{"invalid_argument", &serviceerror.InvalidArgument{}, core.CodeInvalidArgument, "temporal: invalid request"},
		{"permission_denied", &serviceerror.PermissionDenied{}, core.CodePermissionDenied, "temporal: permission denied"},
		{"unavailable", &serviceerror.Unavailable{}, core.CodeUnavailable, "temporal: the service is unavailable"},
		{"deadline_exceeded", &serviceerror.DeadlineExceeded{}, core.CodeUnavailable, "temporal: the service is unavailable"},
		{"rate_limited", &serviceerror.ResourceExhausted{}, core.CodeRateLimited, "temporal: rate limited"},
		{"namespace_not_found", &serviceerror.NamespaceNotFound{}, core.CodeUnavailable, "temporal: the namespace does not exist"},
		{"workflow_not_ready", &serviceerror.WorkflowNotReady{}, core.CodeUnavailable, "temporal: the run is not ready for the call"},
		{"internal", errors.New(marker), core.CodeInternal, "temporal: the server call failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := rt.clientError(op, tc.err)
			var pe *core.Error
			if !errors.As(out, &pe) {
				t.Fatalf("got %T", out)
			}
			if pe.Code != tc.code {
				t.Fatalf("code: %v", pe.Code)
			}
			if pe.Message != tc.msg {
				t.Fatalf("message: %q", pe.Message)
			}
			if pe.Details["op"] != op {
				t.Fatalf("details: %#v", pe.Details)
			}
			if strings.Contains(pe.Message, marker) {
				t.Fatal("cause leaked into message")
			}
		})
	}
}

func TestClientErrorLogsBounded(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, nil)
	rt, _ := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox(), Logger: slog.New(h)})
	long := strings.Repeat("z", 5000)
	_ = rt.clientError("op", errors.New(long))
	if !strings.Contains(buf.String(), truncateLogTextRunes(long)) {
		t.Fatal("bounded log")
	}
}

func TestClientErrorWorkflowNotReadyNoLog(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, nil)
	rt, _ := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox(), Logger: slog.New(h)})
	_ = rt.clientError("resolve", &serviceerror.WorkflowNotReady{})
	if buf.Len() != 0 {
		t.Fatalf("expected no log for WorkflowNotReady, got %q", buf.String())
	}
}

func TestControlOpsMapWorkflowNotReadyUnavailable(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	notReady := &serviceerror.WorkflowNotReady{}
	t.Run("resolve_update", func(t *testing.T) {
		fc := &fakeClient{updateErr: notReady}
		err := testControlRuntime(t, fc).Resolve(ctx, runtime.RunRef{RunID: "r1"}, "g", nil)
		assertWorkflowNotReady(t, err)
	})
	t.Run("resolve_handle", func(t *testing.T) {
		fc := &fakeClient{updateHandle: fakeUpdateHandle{getErr: notReady}}
		err := testControlRuntime(t, fc).Resolve(ctx, runtime.RunRef{RunID: "r1"}, "g", nil)
		assertWorkflowNotReady(t, err)
	})
	t.Run("start", func(t *testing.T) {
		fc := &fakeClient{executeErr: notReady}
		_, err := testControlRuntime(t, fc).Start(ctx, runtime.StartRequest{Flow: runtime.FlowRef{Name: "f"}, RunID: "r1"})
		assertWorkflowNotReady(t, err)
	})
	t.Run("describe", func(t *testing.T) {
		fc := &fakeClient{describeErr: notReady}
		_, err := testControlRuntime(t, fc).Describe(ctx, runtime.RunRef{RunID: "r1"})
		assertWorkflowNotReady(t, err)
	})
	t.Run("cancel", func(t *testing.T) {
		fc := &fakeClient{cancelErr: notReady}
		err := testControlRuntime(t, fc).Cancel(ctx, runtime.RunRef{RunID: "r1"}, "why")
		assertWorkflowNotReady(t, err)
	})
}

func assertWorkflowNotReady(t *testing.T, err error) {
	t.Helper()
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Code != core.CodeUnavailable || pe.Message != "temporal: the run is not ready for the call" {
		t.Fatalf("expected unavailable not-ready, got %v", err)
	}
}

func TestResolveMapsApplicationError(t *testing.T) {
	rt, _ := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	err := rt.mapResolveError("resolve", temporal.NewNonRetryableApplicationError("nope", string(core.CodeNotFound), nil))
	if core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("%v", err)
	}
}
