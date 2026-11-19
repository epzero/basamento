package bus

import (
	"context"
	"runtime"
	"sync"
	"testing"

	"github.com/epzero/basamento/platform"
)

func waitInFlightReplaySend() {
	for i := 0; i < 200_000; i++ {
		runtime.Gosched()
	}
}

func TestLiveSubscriptionEndWhileReplayBlocked(t *testing.T) {
	live := newLiveSubscription(1, 10, func() {}, nil)
	ctx := context.Background()
	msg1 := platform.Message{Subject: "run.r1.d", Seq: 1, Data: []byte("a")}
	msg2 := platform.Message{Subject: "run.r1.d", Seq: 2, Data: []byte("b")}
	live.deliverReplay(ctx, msg1)
	if len(live.ch) != 1 {
		t.Fatalf("buffer len %d", len(live.ch))
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		live.deliverReplay(ctx, msg2)
	}()
	waitInFlightReplaySend()
	live.end()
	wg.Wait()
	got, ok := <-live.messages()
	if !ok || string(got.Data) != "a" {
		t.Fatalf("buffered: ok=%v data=%q", ok, got.Data)
	}
	got2, ok := <-live.messages()
	if ok {
		t.Fatalf("unexpected second %q", got2.Data)
	}
}

func TestLiveSubscriptionOverflowWhileReplayBlocked(t *testing.T) {
	live := newLiveSubscription(1, 10, func() {}, nil)
	ctx := context.Background()
	live.deliverReplay(ctx, platform.Message{Subject: "run.r1.d", Seq: 1, Data: []byte("a")})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		live.deliverReplay(ctx, platform.Message{Subject: "run.r1.d", Seq: 2, Data: []byte("b")})
	}()
	waitInFlightReplaySend()
	live.deliver(ctx, platform.Message{Subject: "run.r1.d", Seq: 11, Data: []byte("live")})
	wg.Wait()
	got, ok := <-live.messages()
	if !ok || string(got.Data) != "a" {
		t.Fatalf("buffered: ok=%v", ok)
	}
	if _, ok := <-live.messages(); ok {
		t.Fatal("channel should be closed")
	}
}

func TestLiveSubscriptionEndIdempotentDeliverNoOp(t *testing.T) {
	live := newLiveSubscription(2, 5, func() {}, nil)
	ctx := context.Background()
	live.end()
	live.end()
	live.deliverReplay(ctx, platform.Message{Seq: 1, Data: []byte("x")})
	live.deliverLive(ctx, platform.Message{Seq: 6, Data: []byte("y")})
	if _, ok := <-live.messages(); ok {
		t.Fatal("expected closed empty channel")
	}
}
