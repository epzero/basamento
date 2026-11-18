package blob

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gocloud.dev/blob"
	"gocloud.dev/blob/fileblob"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

type faultBucket struct {
	inner    bucketBackend
	metaMode string // open, write, close — failures on WriteAll for .meta keys
}

func (f *faultBucket) Exists(ctx context.Context, key string) (bool, error) {
	return f.inner.Exists(ctx, key)
}

func (f *faultBucket) Delete(ctx context.Context, key string) error {
	return f.inner.Delete(ctx, key)
}

func (f *faultBucket) WriteAll(ctx context.Context, key string, p []byte, opts *blob.WriterOptions) error {
	if strings.HasSuffix(key, ".meta") && f.metaMode != "" {
		return errors.New("meta " + f.metaMode + " failed")
	}
	return f.inner.WriteAll(ctx, key, p, opts)
}

func (f *faultBucket) NewReader(ctx context.Context, key string, opts *blob.ReaderOptions) (*blob.Reader, error) {
	return f.inner.NewReader(ctx, key, opts)
}

func (f *faultBucket) NewWriter(ctx context.Context, key string, opts *blob.WriterOptions) (*blob.Writer, error) {
	return f.inner.NewWriter(ctx, key, opts)
}

func (f *faultBucket) Attributes(ctx context.Context, key string) (*blob.Attributes, error) {
	return f.inner.Attributes(ctx, key)
}

func (f *faultBucket) SignedURL(ctx context.Context, key string, opts *blob.SignedURLOptions) (string, error) {
	return f.inner.SignedURL(ctx, key, opts)
}

func (f *faultBucket) Close() error { return f.inner.Close() }

func openFileStore(t *testing.T) (*Store, bucketBackend, string) {
	t.Helper()
	dir := t.TempDir()
	b, err := fileblob.OpenBucket(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	st := &Store{bucket: b, clock: coretest.NewFakeClock(time.Unix(1700000000, 0))}
	return st, b, dir
}

func TestPutMetaFailureNoObject(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"open", "write", "close"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			st, inner, _ := openFileStore(t)
			st.bucket = &faultBucket{inner: inner, metaMode: mode}
			ctx := core.WithTenant(context.Background(), "acme")
			key := ContentKeyOf([]byte("hello"))
			err := st.Put(ctx, key, strings.NewReader("hello"), "text/plain")
			if err == nil {
				t.Fatal("want error")
			}
			ok, err := st.Exists(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				t.Fatal("object should not exist after meta failure")
			}
		})
	}
}

func TestPutMetaFailureRetryRepairsContentType(t *testing.T) {
	t.Parallel()
	st, inner, _ := openFileStore(t)
	st.bucket = &faultBucket{inner: inner, metaMode: "close"}
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("hello"))
	if err := st.Put(ctx, key, strings.NewReader("hello"), "text/plain"); err == nil {
		t.Fatal("first put should fail on meta")
	}
	st.bucket = inner
	if err := st.Put(ctx, key, strings.NewReader("hello"), "text/plain"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	ct, err := st.ContentType(ctx, key)
	if err != nil || ct != "text/plain" {
		t.Fatalf("content type %q err=%v", ct, err)
	}
}

func TestPutConcurrentSameKey(t *testing.T) {
	t.Parallel()
	st, _, dir := openFileStore(t)
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("race"))
	const n = 16
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			errs <- st.Put(ctx, key, bytes.NewReader([]byte("race")), "text/plain")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("put: %v", err)
		}
	}
	ok, err := st.Exists(ctx, key)
	if err != nil || !ok {
		t.Fatalf("exists: %v ok=%v", err, ok)
	}
	var parts int
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".tmp") && !d.IsDir() {
			parts++
		}
		return nil
	})
	if parts != 0 {
		t.Fatalf("leftover tmp files: %d", parts)
	}
}

func TestPutConcurrentFirstContentTypeWins(t *testing.T) {
	t.Parallel()
	st, _, _ := openFileStore(t)
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("same"))
	if err := st.Put(ctx, key, strings.NewReader("same"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := st.Put(ctx, key, strings.NewReader("same"), "text/markdown"); err != nil {
		t.Fatal(err)
	}
	ct, err := st.ContentType(ctx, key)
	if err != nil || ct != "text/plain" {
		t.Fatalf("want first ctype, got %q err=%v", ct, err)
	}
}
