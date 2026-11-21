package blob

import (
	"context"
	"testing"
	"time"

	"gocloud.dev/gcerrors"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

func TestStartUploadCleansSlotWhenCtxCancelledOnSignFailure(t *testing.T) {
	t.Parallel()
	st, inner, _ := openFileStore(t)
	st.embedded = false
	st.bucket = &presignFaultBucket{inner: inner, alwaysErr: &codedDriverErr{code: gcerrors.PermissionDenied, msg: "presign"}}
	ctx, cancel := context.WithCancel(context.Background())
	ctx = core.WithTenant(ctx, "acme")
	cancel()
	_, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 10, ContentType: "text/plain", TTL: time.Minute})
	if err == nil {
		t.Fatal("want error")
	}
	keys, _ := inner.ListKeys(context.Background(), "acme/stage/")
	if len(keys) != 0 {
		t.Fatalf("slot metadata left behind: %v", keys)
	}
}
