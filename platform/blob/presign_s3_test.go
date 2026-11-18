package blob

import (
	"context"
	"os"
	"testing"
	"time"

	"gocloud.dev/blob"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/internal/bdd/s3sim"
	"github.com/epzero/basamento/platform"
)

// presignCountBucket counts SignedURL calls for presign retry tests.
type presignCountBucket struct {
	inner bucketBackend
	n     int
}

func (p *presignCountBucket) SignedURL(ctx context.Context, key string, opts *blob.SignedURLOptions) (string, error) {
	p.n++
	return p.inner.SignedURL(ctx, key, opts)
}
func (p *presignCountBucket) Exists(ctx context.Context, key string) (bool, error) {
	return p.inner.Exists(ctx, key)
}
func (p *presignCountBucket) Delete(ctx context.Context, key string) error {
	return p.inner.Delete(ctx, key)
}
func (p *presignCountBucket) WriteAll(ctx context.Context, key string, b []byte, opts *blob.WriterOptions) error {
	return p.inner.WriteAll(ctx, key, b, opts)
}
func (p *presignCountBucket) NewReader(ctx context.Context, key string, opts *blob.ReaderOptions) (*blob.Reader, error) {
	return p.inner.NewReader(ctx, key, opts)
}
func (p *presignCountBucket) NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (*blob.Writer, error) {
	return p.inner.NewWriter(ctx, key, opts)
}
func (p *presignCountBucket) Attributes(ctx context.Context, key string) (*blob.Attributes, error) {
	return p.inner.Attributes(ctx, key)
}
func (p *presignCountBucket) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	return p.inner.ListKeys(ctx, prefix)
}
func (p *presignCountBucket) ListTenantPrefixes(ctx context.Context) ([]string, error) {
	return p.inner.ListTenantPrefixes(ctx)
}
func (p *presignCountBucket) ListKeysCallback(ctx context.Context, prefix string, fn func(string) error) error {
	return p.inner.ListKeysCallback(ctx, prefix, fn)
}
func (p *presignCountBucket) Close() error { return p.inner.Close() }

func TestPresignRetryOnlyUnimplemented(t *testing.T) {
	t.Parallel()
	s3 := s3sim.Start()
	defer s3.Close()
	_ = os.Setenv("AWS_ACCESS_KEY_ID", "TEST")
	_ = os.Setenv("AWS_SECRET_ACCESS_KEY", "TEST")
	st, _, err := Open(context.Background(), s3.URL, Options{
		Clock:      coretest.NewFakeClock(time.Unix(1700000000, 0)),
		RandReader: coretest.SeededRand(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	st.embedded = false
	cp := &presignCountBucket{inner: st.bucket}
	st.bucket = cp
	ctx := core.WithTenant(context.Background(), "acme")
	u, err := st.StartUpload(ctx, platform.UploadOptions{MaxSize: 100, ContentType: "text/plain", TTL: time.Minute})
	if err != nil || u.URL == "" {
		t.Fatalf("start: %v", err)
	}
	if cp.n != 2 {
		t.Fatalf("SignedURL calls %d want 2", cp.n)
	}
}
