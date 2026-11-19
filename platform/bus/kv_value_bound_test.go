package bus

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestPutKVValueBound(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	ctx := context.Background()
	key := "cursor.r1"
	max := MaxKVValueBytes(DefaultMaxMessageSize)
	okVal := make([]byte, max)
	big := make([]byte, max+1)
	if _, err := st.Put(ctx, key, okVal, 0); err != nil {
		t.Fatal(err)
	}
	got, _, err := st.Get(ctx, key)
	if err != nil || len(got) != max {
		t.Fatalf("get %d err %v", len(got), err)
	}
	if _, err := st.Put(ctx, key, big, 0); err == nil {
		t.Fatal("expected oversize put error")
	} else if string(core.CodeOf(err)) != string(core.CodeInvalidArgument) {
		t.Fatalf("code %s", core.CodeOf(err))
	}
	got2, _, err := st.Get(ctx, key)
	if err != nil || len(got2) != max {
		t.Fatalf("value changed after refused put")
	}
}

func TestCompareAndSwapKVValueBound(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	ctx := context.Background()
	key := "lock.r1"
	max := MaxKVValueBytes(DefaultMaxMessageSize)
	if _, err := st.Put(ctx, key, []byte("seed"), 0); err != nil {
		t.Fatal(err)
	}
	_, rev, err := st.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, max+1)
	if _, err := st.CompareAndSwap(ctx, key, big, rev, 0); err == nil {
		t.Fatal("expected oversize swap error")
	} else if string(core.CodeOf(err)) != string(core.CodeInvalidArgument) {
		t.Fatalf("code %s", core.CodeOf(err))
	}
	okSwap := make([]byte, max)
	if _, err := st.CompareAndSwap(ctx, key, okSwap, rev, 0); err != nil {
		t.Fatal(err)
	}
}
