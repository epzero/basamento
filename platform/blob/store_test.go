package blob

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestPutHelloFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	raw := (&url.URL{Scheme: "file", Path: dir}).String()
	st, _, err := Open(context.Background(), raw, Options{SignKey: []byte("test-signing-key-32-bytes-long")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := core.WithTenant(context.Background(), "acme")
	key := ContentKeyOf([]byte("hello"))
	if err := st.Put(ctx, key, strings.NewReader("hello"), "text/plain"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	ok, err := st.Exists(ctx, key)
	if err != nil || !ok {
		t.Fatalf("Exists: %v ok=%v", err, ok)
	}
}
