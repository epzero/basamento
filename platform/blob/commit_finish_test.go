package blob

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gocloud.dev/blob"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
)

type commitFinishFaultBucket struct {
	inner           bucketBackend
	failDeleteStage bool
	failWriteCommit bool
}

func (c *commitFinishFaultBucket) Exists(ctx context.Context, key string) (bool, error) {
	return c.inner.Exists(ctx, key)
}

func (c *commitFinishFaultBucket) Delete(ctx context.Context, key string) error {
	if c.failDeleteStage && isStagingObjectPath(key) {
		return errors.New("delete stage failed")
	}
	return c.inner.Delete(ctx, key)
}

func (c *commitFinishFaultBucket) WriteAll(ctx context.Context, key string, p []byte, opts *blob.WriterOptions) error {
	if c.failWriteCommit && strings.HasSuffix(key, ".committed") {
		return errors.New("write commit failed")
	}
	return c.inner.WriteAll(ctx, key, p, opts)
}

func (c *commitFinishFaultBucket) NewReader(ctx context.Context, key string, opts *blob.ReaderOptions) (*blob.Reader, error) {
	return c.inner.NewReader(ctx, key, opts)
}

func (c *commitFinishFaultBucket) NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (*blob.Writer, error) {
	return c.inner.NewWriter(ctx, key, opts)
}

func (c *commitFinishFaultBucket) Attributes(ctx context.Context, key string) (*blob.Attributes, error) {
	return c.inner.Attributes(ctx, key)
}

func (c *commitFinishFaultBucket) SignedURL(ctx context.Context, key string, opts *blob.SignedURLOptions) (string, error) {
	return c.inner.SignedURL(ctx, key, opts)
}

func (c *commitFinishFaultBucket) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	return c.inner.ListKeys(ctx, prefix)
}

func (c *commitFinishFaultBucket) ListTenantPrefixes(ctx context.Context) ([]string, error) {
	return c.inner.ListTenantPrefixes(ctx)
}

func (c *commitFinishFaultBucket) ListKeysCallback(ctx context.Context, prefix string, fn func(string) error) error {
	return c.inner.ListKeysCallback(ctx, prefix, fn)
}

func (c *commitFinishFaultBucket) Close() error { return c.inner.Close() }

func isStagingObjectPath(key string) bool {
	return strings.Contains(key, "/stage/stage_") && !strings.HasSuffix(key, ".slot") && !strings.HasSuffix(key, ".committed")
}

func TestCommitSucceedsWhenStagedDeleteFailsAfterRecord(t *testing.T) {
	t.Parallel()
	st, inner, _ := openFileStore(t)
	f := &commitFinishFaultBucket{inner: inner, failDeleteStage: true}
	st.bucket = f
	st.embedded = true
	clock := coretest.NewFakeClock(time.Unix(1700000000, 0))
	st.clock = clock
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 1024, ContentType: "text/plain", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeStagedObjectForTest(ctx, st, "acme", up.StagingKey, []byte("hello"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	key, err := st.Commit(ctx, up.StagingKey)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	want := ContentKeyOf([]byte("hello"))
	if key != want {
		t.Fatalf("key %q want %q", key, want)
	}
	_, err = st.Commit(ctx, up.StagingKey)
	if err == nil || core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("second commit: %v", err)
	}
	ok, err := st.Exists(ctx, want)
	if err != nil || !ok {
		t.Fatalf("blob missing: %v ok=%v", err, ok)
	}
	stagePath, _ := stageObjectPath("acme", up.StagingKey)
	ok, _ = inner.Exists(ctx, stagePath)
	if !ok {
		t.Fatal("staged object should remain until sweep")
	}
	clock.Advance(2 * time.Hour)
	f.failDeleteStage = false
	if err := st.runSweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	ok, _ = inner.Exists(ctx, stagePath)
	if ok {
		t.Fatal("staged object should be removed by sweep")
	}
}

func TestCommitRetryAfterCommitRecordWriteFails(t *testing.T) {
	t.Parallel()
	st, inner, _ := openFileStore(t)
	f := &commitFinishFaultBucket{inner: inner, failWriteCommit: true}
	st.bucket = f
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 1024, ContentType: "text/plain", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeStagedObjectForTest(ctx, st, "acme", up.StagingKey, []byte("hello"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	_, err = st.Commit(ctx, up.StagingKey)
	if err == nil || core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("first commit: %v", err)
	}
	f.failWriteCommit = false
	key, err := st.Commit(ctx, up.StagingKey)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if key != ContentKeyOf([]byte("hello")) {
		t.Fatalf("key %q", key)
	}
}

func TestSpendFailedCommitWritesRecordBeforeDelete(t *testing.T) {
	t.Parallel()
	st, inner, _ := openFileStore(t)
	f := &commitFinishFaultBucket{inner: inner, failDeleteStage: true}
	st.bucket = f
	ctx := core.WithTenant(context.Background(), "acme")
	up, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 1024, ContentType: "text/plain", TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	_ = writeStagedObjectForTest(ctx, st, "acme", up.StagingKey, []byte("hello"), "text/markdown")
	_, err = st.Commit(ctx, up.StagingKey)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("want invalid_argument got %v", err)
	}
	_, err = st.Commit(ctx, up.StagingKey)
	if err == nil || core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("slot should be spent: %v", err)
	}
	cp, _ := commitRecordPath("acme", up.StagingKey)
	ok, _ := inner.Exists(ctx, cp)
	if !ok {
		t.Fatal("commit record should exist")
	}
}
