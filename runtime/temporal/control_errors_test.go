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

func TestClientErrorTable(t *testing.T) {
	rt, _ := New(Options{Payloads: testSecretsCodec(t), Blobs: testBlobStore(t), Outbox: newTestOutbox()})
	marker := "XYZZY_SECRET_MARKER_47"
	cases := []struct {
		err  error
		code core.Code
	}{
		{context.Canceled, core.CodeCancelled},
		{context.DeadlineExceeded, core.CodeTimeout},
		{&serviceerror.NotFound{}, core.CodeNotFound},
		{&serviceerror.WorkflowExecutionAlreadyStarted{}, core.CodeConflict},
		{&serviceerror.InvalidArgument{}, core.CodeInvalidArgument},
		{&serviceerror.PermissionDenied{}, core.CodePermissionDenied},
		{&serviceerror.Unavailable{}, core.CodeUnavailable},
		{&serviceerror.ResourceExhausted{}, core.CodeRateLimited},
		{&serviceerror.NamespaceNotFound{}, core.CodeUnavailable},
		{errors.New(marker), core.CodeInternal},
	}
	for _, tc := range cases {
		out := rt.clientError("op", tc.err)
		if core.CodeOf(out) != tc.code {
			t.Fatalf("%v -> %v", tc.err, out)
		}
		if strings.Contains(out.Error(), marker) {
			t.Fatalf("leaked marker")
		}
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
