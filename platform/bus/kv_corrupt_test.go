package bus

import (
	"context"
	"errors"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestKVCorruptRecord(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	ctx := context.Background()
	key := "cursor.r1"
	const wantMsg = "kv record corrupt"
	if _, err := st.data.Put(ctx, key, []byte{0x01}); err != nil {
		t.Fatal(err)
	}
	_, _, err := st.Get(ctx, key)
	assertCodeMsg(t, err, core.CodeInternal, wantMsg)
	_, err = st.CompareAndSwap(ctx, key, []byte("x"), 1, 0)
	assertCodeMsg(t, err, core.CodeInternal, wantMsg)
	_, err = st.CompareAndSwap(ctx, key, []byte("y"), 0, 0)
	assertCodeMsg(t, err, core.CodeInternal, wantMsg)
	if _, err := st.Put(ctx, key, []byte("ok"), 0); err != nil {
		t.Fatal(err)
	}
	got, _, err := st.Get(ctx, key)
	if err != nil || string(got) != "ok" {
		t.Fatalf("after put: %q err %v", got, err)
	}
}

func assertCodeMsg(t *testing.T, err error, code core.Code, msg string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected error")
	}
	if string(core.CodeOf(err)) != string(code) {
		t.Fatalf("code %s want %s err %v", core.CodeOf(err), code, err)
	}
	var pe *core.Error
	if !errors.As(err, &pe) {
		t.Fatal("not core error")
	}
	if pe.Message != msg {
		t.Fatalf("message %q want %q", pe.Message, msg)
	}
}
