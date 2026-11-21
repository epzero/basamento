package blob

import (
	"context"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestStoreRejectsInvalidTenantBeforeBucket(t *testing.T) {
	t.Parallel()
	var ops int
	drv := newTestBucket(0, 0)
	drv.onOp = func(_, _ string) { ops++ }
	st := openStoreWithTestBucket(t, drv)
	bad := []core.TenantID{
		"",
		" acme",
		"acme ",
		"\xff\xfe",
		"a/b",
		".",
	}
	for _, tid := range bad {
		ops = 0
		ctx := core.WithTenant(context.Background(), tid)
		err := st.Put(ctx, ContentKeyOf([]byte("x")), strings.NewReader("x"), "text/plain")
		if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("Put(%q): code=%v err=%v", tid, core.CodeOf(err), err)
		}
		if ops != 0 {
			t.Fatalf("Put(%q): bucket ops=%d", tid, ops)
		}
	}
}
