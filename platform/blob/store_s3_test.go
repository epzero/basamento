package blob

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/internal/bdd/s3sim"
)

func TestPutHelloS3(t *testing.T) {
	srv := s3sim.Start()
	defer srv.Close()
	_ = os.Setenv("AWS_ACCESS_KEY_ID", "TEST")
	_ = os.Setenv("AWS_SECRET_ACCESS_KEY", "TEST")
	st, _, err := Open(context.Background(), srv.URL, Options{})
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
