package blob

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
)

func openFileStoreOpts(t *testing.T, opts Options) *Store {
	t.Helper()
	dir := t.TempDir()
	raw := (&url.URL{Scheme: "file", Path: dir}).String()
	st, _, err := Open(context.Background(), raw, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("hello"))
	if err := st.Put(ctx, key, strings.NewReader("hello"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestEmbeddedSignedURLRequiresBaseURL(t *testing.T) {
	t.Parallel()
	st := openFileStoreOpts(t, Options{SignKey: []byte("test-signing-key-32-bytes-long")})
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := st.SignedURL(ctx, ContentKeyOf([]byte("hello")), time.Minute)
	if err == nil || core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("got %v", err)
	}
}

func TestEmbeddedSignedURLRequiresSignKey(t *testing.T) {
	t.Parallel()
	st := openFileStoreOpts(t, Options{BaseURL: "https://example.test" + HandlerRoute()})
	ctx := core.WithTenant(context.Background(), "acme")
	_, err := st.SignedURL(ctx, ContentKeyOf([]byte("hello")), time.Minute)
	if err == nil || core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("got %v", err)
	}
}
