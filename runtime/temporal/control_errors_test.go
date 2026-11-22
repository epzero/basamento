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

func TestResolveMapsApplicationError(t *testing.T) {
	rt, _ := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	err := rt.mapResolveError("resolve", temporal.NewNonRetryableApplicationError("nope", string(core.CodeNotFound), nil))
	if core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("%v", err)
	}
}
