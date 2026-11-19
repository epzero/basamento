package bus

import (
	"context"
	"testing"

	"github.com/epzero/basamento/platform"
)

func TestStoreSparseReplayThenLive(t *testing.T) {
	st, _, _ := openTestEmbeddedBuf(t, 4)
	ctx := context.Background()
	a := "run.r1.sa"
	b := "run.r1.sb"
	if _, err := st.Publish(ctx, a, []byte("a1")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := st.Publish(ctx, b, []byte{byte('0' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Publish(ctx, a, []byte("a2")); err != nil {
		t.Fatal(err)
	}
	ch, err := st.Subscribe(ctx, a, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := recvSeqs(t, ch, 2)
	if got[0] != 1 || got[1] != 12 {
		t.Fatalf("replay %v", got)
	}
	liveSeq, err := st.Publish(ctx, a, []byte("live"))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := <-ch
	if !ok {
		t.Fatalf("subscription ended early after replay %v", got)
	}
	if m.Seq != liveSeq {
		t.Fatalf("live seq=%d want %d", m.Seq, liveSeq)
	}
}

func TestStorePurgeThenLiveDelivered(t *testing.T) {
	st, _, _ := openTestEmbeddedBuf(t, 4)
	ctx := context.Background()
	subject := "run.r1.pur"
	if _, err := st.Publish(ctx, subject, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := st.Purge(ctx, subject); err != nil {
		t.Fatal(err)
	}
	ch, err := st.Subscribe(ctx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := st.Publish(ctx, subject, []byte("live"))
	if err != nil {
		t.Fatal(err)
	}
	m, ok := <-ch
	if !ok {
		t.Fatal("subscription ended before first live after purge")
	}
	if m.Seq != seq {
		t.Fatalf("seq=%d want %d", m.Seq, seq)
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected second message")
		}
	default:
	}
}

func TestStoreLiveImmediatelyAfterSubscribeDuringSparseReplay(t *testing.T) {
	st, _, _ := openTestEmbeddedBuf(t, 4)
	ctx := context.Background()
	a := "run.r1.im"
	b := "run.r1.imx"
	if _, err := st.Publish(ctx, a, []byte("a1")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := st.Publish(ctx, b, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Publish(ctx, a, []byte("a2")); err != nil {
		t.Fatal(err)
	}
	ch, err := st.Subscribe(ctx, a, 0)
	if err != nil {
		t.Fatal(err)
	}
	liveSeq, err := st.Publish(ctx, a, []byte("live"))
	if err != nil {
		t.Fatal(err)
	}
	got := recvSeqs(t, ch, 3)
	if got[0] != 1 || got[1] != 7 || got[2] != liveSeq {
		t.Fatalf("got %v live=%d", got, liveSeq)
	}
}

func TestStoreReplayBacklogOverflowShape(t *testing.T) {
	st, _, _ := openTestEmbeddedBuf(t, 4)
	ctx := context.Background()
	subject := "run.r1.big"
	for i := 0; i < 6; i++ {
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
	storeAssertRecvSeqs(t, ch, []uint64{1, 2, 3, 4, 5, 6})
}

func TestStoreTwoLiveFitThenOverflowDuringReplay(t *testing.T) {
	st, _, _ := openTestEmbeddedBuf(t, 4)
	ctx := context.Background()
	subject := "run.r1.fit"
	for i := 0; i < 2; i++ {
		if _, err := st.Publish(ctx, subject, []byte{byte('a' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := st.Subscribe(ctx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := <-ch; !ok {
		t.Fatal("expected first replay")
	}
	if _, err := st.Publish(ctx, subject, []byte("l1")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, subject, []byte("l2")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, subject, []byte("overflow")); err != nil {
		t.Fatal(err)
	}
	storeRecvSeqsOnly(t, ch, []uint64{2, 3, 4})
}

func storeRecvSeqsOnly(t *testing.T, ch <-chan platform.Message, want []uint64) {
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
}

// recvSeqs mirrors recvNSeqs in internal/bdd/bustest/fake_sparse_test.go (test packages cannot share helpers).
func recvSeqs(t *testing.T, ch <-chan platform.Message, n int) []uint64 {
	t.Helper()
	var got []uint64
	for len(got) < n {
		m, ok := <-ch
		if !ok {
			t.Fatalf("closed after %v want %d", got, n)
		}
		got = append(got, m.Seq)
	}
	return got
}
