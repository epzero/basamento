package blob

import (
	"context"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestContentTypeTenantFromContext(t *testing.T) {
	t.Parallel()
	st, _, _ := openFileStore(t)
	key := ContentKeyOf([]byte("secret"))
	ctxAcme := core.WithTenant(context.Background(), "acme")
	if err := st.Put(ctxAcme, key, strings.NewReader("secret"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	ctxGlobex := core.WithTenant(context.Background(), "globex")
	ct, err := st.ContentType(ctxGlobex, key)
	if err != nil || ct != "" {
		t.Fatalf("other tenant want empty, got %q err=%v", ct, err)
	}
	ok, err := st.Exists(ctxGlobex, key)
	if err != nil || ok {
		t.Fatalf("globex should not see acme blob: ok=%v err=%v", ok, err)
	}
	_, err = st.ContentType(context.Background(), key)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("no tenant: %v", err)
	}
	ct, err = st.ContentType(ctxAcme, key)
	if err != nil || ct != "text/plain" {
		t.Fatalf("acme: %q err=%v", ct, err)
	}
}
