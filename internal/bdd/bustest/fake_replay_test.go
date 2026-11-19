package bustest

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/bus"
)

func TestFakeReplayLongerThanBufferDoesNotHangClose(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
	ctx := context.Background()
	subject := "run.r1.replay"
	for i := 1; i <= 10; i++ {
		if _, err := f.Publish(ctx, subject, []byte{byte('0' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := f.Subscribe(ctx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if _, ok := <-ch; !ok {
			t.Fatalf("ended at %d", i)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestFakeThreeSubsOneOverflow(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 2, MaxMessageSize: bus.DefaultMaxMessageSize})
	ctx := context.Background()
	subject := "run.r1.multi"
	c1, _ := f.Subscribe(ctx, subject, 0)
	c2, _ := f.Subscribe(ctx, subject, 0)
	c3, _ := f.Subscribe(ctx, subject, 0)
	if _, err := f.Publish(ctx, subject, []byte("m")); err != nil {
		t.Fatal(err)
	}
	read := func(ch <-chan platform.Message) []byte {
		m, ok := <-ch
		if !ok {
			t.Fatal("closed")
		}
		return append([]byte(nil), m.Data...)
	}
	_ = read(c1)
	got2 := read(c2)
	got3 := read(c3)
	if string(got2) != "m" || string(got3) != "m" {
		t.Fatalf("missed duplicate %q %q", got2, got3)
	}
}

func TestFakeCancelDuringReplayPublishReturns(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 2, MaxMessageSize: bus.DefaultMaxMessageSize})
	subject := "run.r1.cancel"
	root := context.Background()
	for i := 1; i <= 8; i++ {
		if _, err := f.Publish(root, subject, []byte{byte('a' + i - 1)}); err != nil {
			t.Fatal(err)
		}
	}
	subCtx, cancel := context.WithCancel(root)
	_, err := f.Subscribe(subCtx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	pubCtx, pubCancel := context.WithTimeout(root, 2*time.Second)
	defer pubCancel()
	if _, err := f.Publish(pubCtx, subject, []byte("live")); err != nil {
		t.Fatalf("publish after cancel: %v", err)
	}
}

func TestFakePublishNotBlockedBySlowReplay(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
	subject := "run.r1.slow"
	ctx := context.Background()
	for i := 1; i <= 10; i++ {
		if _, err := f.Publish(ctx, subject, []byte{byte('0' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := f.Subscribe(ctx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	pubCtx, pubCancel := context.WithTimeout(ctx, 2*time.Second)
	defer pubCancel()
	if _, err := f.Publish(pubCtx, subject, []byte("m11")); err != nil {
		t.Fatalf("publish hung: %v", err)
	}
	if _, err := f.Publish(pubCtx, subject, []byte("m12")); err != nil {
		t.Fatalf("second publish hung: %v", err)
	}
	_ = ch
}

func TestFakeLiveQueuedInReplayOrder(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 1, MaxMessageSize: bus.DefaultMaxMessageSize})
	subject := "run.r1.order"
	ctx := context.Background()
	for _, label := range []string{"r1", "r2"} {
		if _, err := f.Publish(ctx, subject, []byte(label)); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := f.Subscribe(ctx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	pubDone := make(chan struct{})
	go func() {
		_, _ = f.Publish(ctx, subject, []byte("live1"))
		close(pubDone)
	}()
	want := []string{"r1", "r2", "live1"}
	for i, label := range want {
		m, ok := <-ch
		if !ok {
			t.Fatalf("ended at %d", i)
		}
		if string(m.Data) != label {
			t.Fatalf("got %q want %q", m.Data, label)
		}
	}
	<-pubDone
}
