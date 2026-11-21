package blob

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	"github.com/epzero/basamento/core"
)

func TestMapErrorCodes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		err       error
		wantCode  core.Code
		wantMsg   string
		retryable bool
	}{
		{name: "NotFound", err: bucketReaderErr(t, gcerrors.NotFound), wantCode: core.CodeNotFound, wantMsg: "blob not found"},
		{name: "InvalidArgument", err: bucketWriterErr(t, gcerrors.InvalidArgument), wantCode: core.CodeInvalidArgument, wantMsg: "invalid blob request"},
		{name: "PermissionDenied", err: bucketWriterErr(t, gcerrors.PermissionDenied), wantCode: core.CodePermissionDenied, wantMsg: "blob access denied"},
		{name: "Canceled", err: bucketWriterErr(t, gcerrors.Canceled), wantCode: core.CodeCancelled, wantMsg: "blob operation cancelled"},
		{name: "DeadlineExceeded", err: bucketWriterErr(t, gcerrors.DeadlineExceeded), wantCode: core.CodeUnavailable, wantMsg: "blob store unavailable", retryable: true},
		{name: "Unknown", err: bucketWriterErr(t, gcerrors.Unknown), wantCode: core.CodeUnavailable, wantMsg: "blob store error", retryable: true},
		{name: "Internal", err: bucketWriterErr(t, gcerrors.Internal), wantCode: core.CodeUnavailable, wantMsg: "blob store error", retryable: true},
		{name: "ResourceExhausted", err: bucketWriterErr(t, gcerrors.ResourceExhausted), wantCode: core.CodeUnavailable, wantMsg: "blob store error", retryable: true},
		{name: "FailedPrecondition", err: bucketWriterErr(t, gcerrors.FailedPrecondition), wantCode: core.CodeUnavailable, wantMsg: "blob store error", retryable: true},
		{name: "OsNotExist", err: os.ErrNotExist, wantCode: core.CodeNotFound, wantMsg: "blob not found"},
		{name: "NoSuchKey", err: errors.New("operation error S3: NoSuchKey: missing"), wantCode: core.CodeNotFound, wantMsg: "blob not found"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := mapError(tc.err)
			if got == nil {
				t.Fatal("nil")
			}
			if core.CodeOf(got) != tc.wantCode {
				t.Fatalf("code %v want %v", core.CodeOf(got), tc.wantCode)
			}
			if !strings.Contains(got.Error(), tc.wantMsg) {
				t.Fatalf("message %q want substring %q", got.Error(), tc.wantMsg)
			}
			if core.IsRetryable(got) != tc.retryable {
				t.Fatalf("retryable %v want %v", core.IsRetryable(got), tc.retryable)
			}
		})
	}
}

func bucketReaderErr(t *testing.T, code gcerrors.ErrorCode) error {
	t.Helper()
	bkt := blob.NewBucket(newTestBucket(0, code))
	if code == gcerrors.NotFound {
		_, err := bkt.NewReader(context.Background(), "missing", nil)
		return err
	}
	_, err := bkt.NewReader(context.Background(), "missing", nil)
	return err
}

func bucketWriterErr(t *testing.T, code gcerrors.ErrorCode) error {
	t.Helper()
	drv := newTestBucket(1, code)
	bkt := blob.NewBucket(drv)
	w, err := bkt.NewWriter(context.Background(), "obj", &blob.WriterOptions{})
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte("x")); err != nil {
		return err
	}
	return w.Close()
}

func TestMapErrorContextCanceled(t *testing.T) {
	t.Parallel()
	got := mapError(context.Canceled)
	if core.CodeOf(got) != core.CodeCancelled {
		t.Fatalf("code %v", core.CodeOf(got))
	}
}

func TestMapOpenErrorUnavailableRetryable(t *testing.T) {
	t.Parallel()
	got := mapOpenError(bucketWriterErr(t, gcerrors.Unknown))
	if core.CodeOf(got) != core.CodeUnavailable || !core.IsRetryable(got) {
		t.Fatalf("got %v retryable=%v", got, core.IsRetryable(got))
	}
}

func TestErrUnavailableRetryable(t *testing.T) {
	t.Parallel()
	if !core.IsRetryable(errUnavailable("blob store unavailable")) {
		t.Fatal("not retryable")
	}
}
