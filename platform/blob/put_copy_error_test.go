package blob

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"gocloud.dev/blob"
	"gocloud.dev/gcerrors"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/bdd/s3sim"
)

func TestPutBackendWriteFailureUnknownUnavailable(t *testing.T) {
	t.Parallel()
	srv := s3sim.Start()
	_ = os.Setenv("AWS_ACCESS_KEY_ID", "TEST")
	_ = os.Setenv("AWS_SECRET_ACCESS_KEY", "TEST")
	st, _, err := Open(context.Background(), srv.URL, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := core.WithTenant(context.Background(), "acme")
	body := make([]byte, 2*1024*1024)
	for i := range body {
		body[i] = byte(i % 251)
	}
	key := ContentKeyOf(body)
	done := make(chan error, 1)
	go func() {
		done <- st.Put(ctx, key, bytes.NewReader(body), "application/octet-stream")
	}()
	srv.Close()
	err = <-done
	if err == nil || core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("want unavailable, got %v", err)
	}
	ok, _ := st.Exists(ctx, key)
	if ok {
		t.Fatal("object leaked")
	}
}

func TestPutBackendWriteFailureResourceExhaustedUnavailable(t *testing.T) {
	t.Parallel()
	st, inner, _ := openFileStore(t)
	st.bucket = &failNewWriterBucket{inner: inner, err: errors.New("resource exhausted")}
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("x"))
	putErr := st.Put(ctx, key, strings.NewReader("x"), "text/plain")
	if putErr == nil || core.CodeOf(putErr) != core.CodeUnavailable {
		t.Fatalf("want unavailable, got %v", putErr)
	}
	if gcerrors.Code(putErr) != gcerrors.Unknown {
		t.Fatalf("store open/write error code %v", gcerrors.Code(putErr))
	}
}

func TestPutReaderFailureStillInvalidArgument(t *testing.T) {
	t.Parallel()
	st, _, _ := openFileStore(t)
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("hello"))
	err := st.Put(ctx, key, &failAfterReader{b: []byte("hello"), limit: 2}, "text/plain")
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}
}

type failNewWriterBucket struct {
	inner bucketBackend
	err   error
}

func (f *failNewWriterBucket) Exists(ctx context.Context, key string) (bool, error) {
	return f.inner.Exists(ctx, key)
}

func (f *failNewWriterBucket) Delete(ctx context.Context, key string) error {
	return f.inner.Delete(ctx, key)
}

func (f *failNewWriterBucket) WriteAll(ctx context.Context, key string, p []byte, opts *blob.WriterOptions) error {
	return f.inner.WriteAll(ctx, key, p, opts)
}

func (f *failNewWriterBucket) NewReader(ctx context.Context, key string, opts *blob.ReaderOptions) (*blob.Reader, error) {
	return f.inner.NewReader(ctx, key, opts)
}

func (f *failNewWriterBucket) NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (*blob.Writer, error) {
	return nil, f.err
}

func (f *failNewWriterBucket) Attributes(ctx context.Context, key string) (*blob.Attributes, error) {
	return f.inner.Attributes(ctx, key)
}

func (f *failNewWriterBucket) SignedURL(ctx context.Context, key string, opts *blob.SignedURLOptions) (string, error) {
	return f.inner.SignedURL(ctx, key, opts)
}

func (f *failNewWriterBucket) Close() error { return f.inner.Close() }
