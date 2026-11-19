package bus

import (
	"context"
	"testing"

	"github.com/epzero/basamento/platform"
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
	storeAssertRecvSeqs(t, ch, []uint64{1, 2, 3, 4})
}

func TestStoreLiveOverflowShapes(t *testing.T) {
	t.Run("a_overflowBeforeReplayRequested", testStoreOverflowBeforeReplay)
	t.Run("b_liveDuringReplay", TestStoreLiveOverflowDuringReplay)
	t.Run("e_twoLiveDuringReplay", testStoreTwoLiveDuringReplay)
}

func testStoreOverflowBeforeReplay(t *testing.T) {
	st, _, _ := openTestEmbeddedBuf(t, 4)
	ctx := context.Background()
	subject := "run.r1.sh.a"
	for i := 0; i < 4; i++ {
		if _, err := st.Publish(ctx, subject, []byte{byte('a' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := st.Subscribe(ctx, subject, 4)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, err := st.Publish(ctx, subject, []byte{byte('l' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Publish(ctx, subject, []byte("overflow")); err != nil {
		t.Fatal(err)
	}
	storeAssertRecvSeqs(t, ch, []uint64{5, 6, 7, 8})
}

func testStoreTwoLiveDuringReplay(t *testing.T) {
	st, _, _ := openTestEmbeddedBuf(t, 4)
	ctx := context.Background()
	subject := "run.r1.sh.e"
	for i := 0; i < 4; i++ {
		if _, err := st.Publish(ctx, subject, []byte{byte('a' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := st.Subscribe(ctx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, subject, []byte("live1")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, subject, []byte("live2")); err != nil {
		t.Fatal(err)
	}
	storeAssertRecvSeqs(t, ch, []uint64{1, 2, 3, 4})
}

func storeAssertRecvSeqs(t *testing.T, ch <-chan platform.Message, want []uint64) {
	t.Helper()
	for i, w := range want {
		m, ok := <-ch
		if !ok {
			t.Fatalf("ended at %d want seq %d", i, w)
		}
		if m.Seq != w {
			t.Fatalf("[%d] seq=%d want %d", i, m.Seq, w)
		}
	}
	if _, ok := <-ch; ok {
		t.Fatal("subscription should have ended on live overflow")
	}
}
