package bus

import (
	"context"
	"testing"
)

func TestStoreLiveOverflowDuringReplay(t *testing.T) {
	st, _, _ := openTestEmbeddedBuf(t, 4)
	ctx := context.Background()
	subject := "run.r1.lv"
	for i := 0; i < 4; i++ {
		if _, err := st.Publish(ctx, subject, []byte{byte('a' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := st.Subscribe(ctx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, subject, []byte("live")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, ok := <-ch; !ok {
			t.Fatalf("ended at %d", i)
		}
	}
	if _, ok := <-ch; ok {
		t.Fatal("subscription should end on live overflow")
	}
}
