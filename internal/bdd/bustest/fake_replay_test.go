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
