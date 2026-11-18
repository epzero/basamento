package bus

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestCallsUnavailableWhenDisconnected(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	st.ForceDisconnect()
	ctx := context.Background()
	subject := "run.r1.down"
	key := "lock.r1"
	check := func(err error, op string) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s succeeded", op)
		}
		if string(core.CodeOf(err)) != string(core.CodeUnavailable) {
			t.Fatalf("%s code %s", op, core.CodeOf(err))
		}
		if string(core.CodeOf(err)) == string(core.CodeTimeout) {
			t.Fatalf("%s timed out", op)
		}
	}
	if _, err := st.Publish(ctx, subject, []byte("x")); err != nil {
		check(err, "Publish")
	}
	if _, err := st.LastSeq(ctx, subject); err != nil {
		check(err, "LastSeq")
	}
	if err := st.Purge(ctx, subject); err != nil {
		check(err, "Purge")
	}
	if _, err := st.Subscribe(ctx, subject, 0); err != nil {
		check(err, "Subscribe")
	}
	if _, _, err := st.Get(ctx, key); err != nil {
		check(err, "Get")
	}
	if _, err := st.Put(ctx, key, []byte("v"), 0); err != nil {
		check(err, "Put")
	}
	if _, err := st.CompareAndSwap(ctx, key, []byte("v"), 0, 0); err != nil {
		check(err, "CompareAndSwap")
	}
	if err := st.Delete(ctx, key); err != nil {
		check(err, "Delete")
	}
}
