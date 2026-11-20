package bustest

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/bus"
)

func TestFakeSparseReplayThenLive(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
	ctx := context.Background()
	a := "run.r1.sa"
	b := "run.r1.sb"
	if _, err := f.Publish(ctx, a, []byte("a1")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := f.Publish(ctx, b, []byte{byte('0' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Publish(ctx, a, []byte("a2")); err != nil {
		t.Fatal(err)
	}
	ch, err := f.Subscribe(ctx, a, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := recvNSeqs(t, ch, 2)
	if got[0] != 1 || got[1] != 12 {
		t.Fatalf("replay %v", got)
	}
	liveSeq, err := f.Publish(ctx, a, []byte("live"))
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

// TestFakeSparseAfterGapKeepsLiveSlot mirrors the store: with after above LastSeq
// (after=10, LastSeq=5) publishes 6..10 are skipped by the subscription and must
// not consume buffer slots, so 11..14 fit and 15 ends the subscription instead
// of a live message being lost (REQ-BUS-004, REQ-BUS-010).
func TestFakeSparseAfterGapKeepsLiveSlot(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
	ctx := context.Background()
	subject := "run.r1.gap"
	for i := 0; i < 5; i++ {
		if _, err := f.Publish(ctx, subject, []byte{byte('0' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := f.Subscribe(ctx, subject, 10)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := f.Publish(ctx, subject, []byte{byte('a' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4; i++ {
		if _, err := f.Publish(ctx, subject, []byte{byte('l' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Publish(ctx, subject, []byte("overflow")); err != nil {
		t.Fatal(err)
	}
	assertFakeRecvSeqs(t, ch, []uint64{11, 12, 13, 14})
}

func TestFakePurgeThenLiveDelivered(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
	ctx := context.Background()
	subject := "run.r1.pur"
	if _, err := f.Publish(ctx, subject, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := f.Purge(ctx, subject); err != nil {
		t.Fatal(err)
	}
	ch, err := f.Subscribe(ctx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := f.Publish(ctx, subject, []byte("live"))
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
}

func TestFakeLiveImmediatelyAfterSubscribeDuringSparseReplay(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
	ctx := context.Background()
	a, b := "run.r1.im", "run.r1.imx"
	_, _ = f.Publish(ctx, a, []byte("a1"))
	for i := 0; i < 5; i++ {
		_, _ = f.Publish(ctx, b, []byte("x"))
	}
	_, _ = f.Publish(ctx, a, []byte("a2"))
	ch, err := f.Subscribe(ctx, a, 0)
	if err != nil {
		t.Fatal(err)
	}
	liveSeq, _ := f.Publish(ctx, a, []byte("live"))
	got := recvNSeqs(t, ch, 3)
	if got[0] != 1 || got[1] != 7 || got[2] != liveSeq {
		t.Fatalf("got %v live=%d", got, liveSeq)
	}
}

func TestFakeReplayBacklogOverflowShape(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
	ctx := context.Background()
	subject := "run.r1.big"
	for i := 0; i < 6; i++ {
		_, _ = f.Publish(ctx, subject, []byte{byte('a' + i)})
	}
	ch, err := f.Subscribe(ctx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.Publish(ctx, subject, []byte("live"))
	assertFakeRecvSeqs(t, ch, []uint64{1, 2, 3, 4, 5, 6})
}

func TestFakeTwoLiveFitThenOverflowDuringReplay(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
	ctx := context.Background()
	subject := "run.r1.fit"
	for i := 0; i < 2; i++ {
		_, _ = f.Publish(ctx, subject, []byte{byte('a' + i)})
	}
	ch, err := f.Subscribe(ctx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := <-ch; !ok {
		t.Fatal("expected first replay")
	}
	_, _ = f.Publish(ctx, subject, []byte("l1"))
	_, _ = f.Publish(ctx, subject, []byte("l2"))
	_, _ = f.Publish(ctx, subject, []byte("overflow"))
	assertFakeRecvSeqsOnly(t, ch, []uint64{2, 3, 4})
}

func assertFakeRecvSeqs(t *testing.T, ch <-chan platform.Message, want []uint64) {
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
		t.Fatal("subscription should have ended on overflow")
	}
}

func assertFakeRecvSeqsOnly(t *testing.T, ch <-chan platform.Message, want []uint64) {
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

// recvNSeqs mirrors recvSeqs in platform/bus/replay_sparse_test.go (test packages cannot share helpers).
func recvNSeqs(t *testing.T, ch <-chan platform.Message, n int) []uint64 {
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
