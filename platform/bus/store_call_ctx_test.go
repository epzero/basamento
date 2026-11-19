package bus

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
)

func TestStoreCallsRespectContext(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	subject := "run.r1.ctx"
	key := "cursor.r1"
	calls := []struct {
		name string
		run  func(context.Context) error
	}{
		{"Publish", func(c context.Context) error { _, err := st.Publish(c, subject, []byte("x")); return err }},
		{"Subscribe", func(c context.Context) error { _, err := st.Subscribe(c, subject, 0); return err }},
		{"LastSeq", func(c context.Context) error { _, err := st.LastSeq(c, subject); return err }},
		{"Purge", func(c context.Context) error { return st.Purge(c, subject) }},
		{"Get", func(c context.Context) error { _, _, err := st.Get(c, key); return err }},
		{"Put", func(c context.Context) error { _, err := st.Put(c, key, []byte("v"), 0); return err }},
		{"CompareAndSwap", func(c context.Context) error { _, err := st.CompareAndSwap(c, key, []byte("v"), 0, 0); return err }},
		{"Delete", func(c context.Context) error { return st.Delete(c, key) }},
	}
	for _, tc := range calls {
		t.Run("cancelled/"+tc.name, func(t *testing.T) {
			err := tc.run(ctx)
			if err == nil || string(core.CodeOf(err)) != string(core.CodeCancelled) {
				t.Fatalf("got %v", err)
			}
		})
	}
	deadCtx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	for _, tc := range calls {
		t.Run("timeout/"+tc.name, func(t *testing.T) {
			err := tc.run(deadCtx)
			if err == nil || string(core.CodeOf(err)) != string(core.CodeTimeout) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
