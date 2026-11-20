package bus

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestOpenEmbeddedCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	st, err := OpenEmbedded(ctx, dir, Options{})
	if err == nil {
		_ = st.Close()
		t.Fatal("expected error")
	}
	if string(core.CodeOf(err)) != string(core.CodeCancelled) {
		t.Fatalf("got %s", core.CodeOf(err))
	}
}
