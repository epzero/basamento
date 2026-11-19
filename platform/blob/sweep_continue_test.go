package blob

import (
	"context"
	"errors"
	"testing"
	"time"

	"gocloud.dev/blob"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

const (
	testStageA = "stage_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	testStageB = "stage_BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	testStageC = "stage_CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"
)

type sweepSlotFailBucket struct {
	inner     bucketBackend
	failStage string
}

func (s *sweepSlotFailBucket) Delete(ctx context.Context, key string) error {
	if s.failStage != "" && key == s.failStage {
		return errors.New("sweep delete failed")
	}
	return s.inner.Delete(ctx, key)
}

func (s *sweepSlotFailBucket) Exists(ctx context.Context, key string) (bool, error) {
	return s.inner.Exists(ctx, key)
}

func (s *sweepSlotFailBucket) WriteAll(ctx context.Context, key string, p []byte, opts *blob.WriterOptions) error {
	return s.inner.WriteAll(ctx, key, p, opts)
}

func (s *sweepSlotFailBucket) NewReader(ctx context.Context, key string, opts *blob.ReaderOptions) (*blob.Reader, error) {
	return s.inner.NewReader(ctx, key, opts)
}

func (s *sweepSlotFailBucket) NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (*blob.Writer, error) {
	return s.inner.NewWriter(ctx, key, opts)
}

func (s *sweepSlotFailBucket) Attributes(ctx context.Context, key string) (*blob.Attributes, error) {
	return s.inner.Attributes(ctx, key)
}

func (s *sweepSlotFailBucket) SignedURL(ctx context.Context, key string, opts *blob.SignedURLOptions) (string, error) {
	return s.inner.SignedURL(ctx, key, opts)
}

func (s *sweepSlotFailBucket) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	return s.inner.ListKeys(ctx, prefix)
}

func (s *sweepSlotFailBucket) ListTenantPrefixes(ctx context.Context) ([]string, error) {
	return s.inner.ListTenantPrefixes(ctx)
}

func (s *sweepSlotFailBucket) ListKeysCallback(ctx context.Context, prefix string, fn func(string) error) error {
	return s.inner.ListKeysCallback(ctx, prefix, fn)
}

func (s *sweepSlotFailBucket) Close() error { return s.inner.Close() }

func plantExpiredSlot(ctx context.Context, st *Store, tenant, stageKey string, expired time.Time) error {
	meta := slotMeta{Max: 10, ContentType: "text/plain", Expires: expired.Unix()}
	raw, err := encodeSlotMeta(meta)
	if err != nil {
		return err
	}
	mp, err := slotMetaPath(core.TenantID(tenant), stageKey)
	if err != nil {
		return err
	}
	if err := st.bucket.WriteAll(ctx, mp, raw, nil); err != nil {
		return err
	}
	sp, err := stageObjectPath(core.TenantID(tenant), stageKey)
	if err != nil {
		return err
	}
	return st.bucket.WriteAll(ctx, sp, []byte("x"), nil)
}

func TestSweepContinuesAfterSlotDeleteFails(t *testing.T) {
	t.Parallel()
	st, inner, _ := openFileStore(t)
	st.embedded = true
	clock := coretest.NewFakeClock(time.Unix(1700000000, 0))
	st.clock = clock
	expired := clock.Now().Add(-time.Hour)
	ctx := context.Background()
	for _, k := range []string{testStageA, testStageB, testStageC} {
		if err := plantExpiredSlot(ctx, st, "acme", k, expired); err != nil {
			t.Fatal(err)
		}
	}
	failPath, _ := stageObjectPath("acme", testStageA)
	fb := &sweepSlotFailBucket{inner: inner, failStage: failPath}
	st.bucket = fb
	err := st.runSweep(ctx)
	if err == nil {
		t.Fatal("want sweep error")
	}
	for _, k := range []string{testStageB, testStageC} {
		mp, _ := slotMetaPath("acme", k)
		ok, _ := inner.Exists(ctx, mp)
		if ok {
			t.Fatalf("slot %s should be swept", k)
		}
	}
	mpA, _ := slotMetaPath("acme", testStageA)
	if ok, _ := inner.Exists(ctx, mpA); !ok {
		t.Fatal("stuck slot metadata should remain")
	}
	fb.failStage = ""
	clock.Advance(sweepRetryWait + time.Second)
	st.sweepRetryAt = time.Time{}
	if err := st.runSweep(ctx); err != nil {
		t.Fatalf("retry sweep: %v", err)
	}
	if ok, _ := inner.Exists(ctx, mpA); ok {
		t.Fatal("stuck slot should be swept on retry")
	}
}
