package bus

import (
	"context"
	"testing"

	"github.com/epzero/basamento/platform"
)

func TestLiveSubscriptionOverflowBeforeDelivery(t *testing.T) {
	live := newLiveSubscription("run.r1.d", 4, 4, 4, func() {}, nil)
	live.replayDone = true
	for seq := uint64(5); seq <= 9; seq++ {
		live.reserve()
		live.confirm(seq)
	}
	if !live.dropLive || live.dropFrom != 9 {
		t.Fatalf("drop=%v from=%d", live.dropLive, live.dropFrom)
	}
	ctx := context.Background()
	for seq := uint64(5); seq <= 8; seq++ {
		live.deliverLive(ctx, platform.Message{Subject: "run.r1.d", Seq: seq, Data: []byte("x")})
	}
	if len(live.ch) != 4 {
		t.Fatalf("buffer len %d", len(live.ch))
	}
	live.deliverLive(ctx, platform.Message{Subject: "run.r1.d", Seq: 9, Data: []byte("overflow")})
	if !live.ended {
		t.Fatal("expected subscription ended")
	}
	for seq := uint64(5); seq <= 8; seq++ {
		m, ok := <-live.messages()
		if !ok {
			t.Fatalf("ended early want seq %d", seq)
		}
		if m.Seq != seq {
			t.Fatalf("seq %d want %d", m.Seq, seq)
		}
	}
	if _, ok := <-live.messages(); ok {
		t.Fatal("overflow must not be delivered")
	}
}

func TestLiveReserveReleaseOnFailedPublish(t *testing.T) {
	live := newLiveSubscription("run.r1.d", 4, 0, 0, func() {}, nil)
	live.replayDone = true
	live.reserve()
	live.release()
	if live.liveAhead != 0 {
		t.Fatalf("ahead=%d", live.liveAhead)
	}
}

func TestLiveConfirmEndedNoOp(t *testing.T) {
	live := newLiveSubscription("run.r1.d", 4, 0, 0, func() {}, nil)
	live.ended = true
	live.confirm(1)
	if live.liveAhead != 0 {
		t.Fatal("ended confirm should not count")
	}
}

func TestLiveConfirmDropFromOnce(t *testing.T) {
	live := newLiveSubscription("run.r1.d", 4, 4, 4, func() {}, nil)
	live.replayDone = true
	for seq := uint64(5); seq <= 8; seq++ {
		live.reserve()
		live.confirm(seq)
	}
	if live.dropLive {
		t.Fatal("four live publishes should fit")
	}
	live.reserve()
	live.confirm(9)
	if !live.dropLive || live.dropFrom != 9 {
		t.Fatalf("drop=%v from=%d", live.dropLive, live.dropFrom)
	}
	before := live.dropFrom
	live.confirm(10)
	if live.dropFrom != before {
		t.Fatalf("dropFrom changed to %d", live.dropFrom)
	}
}

func TestLiveDeliverBeforeConfirmNoInflate(t *testing.T) {
	live := newLiveSubscription("run.r1.d", 4, 0, 0, func() {}, nil)
	live.replayDone = true
	ctx := context.Background()
	live.reserve()
	live.confirm(1)
	live.deliverLive(ctx, platform.Message{Subject: "run.r1.d", Seq: 1, Data: []byte("x")})
	if live.liveAhead != 0 {
		t.Fatalf("ahead=%d", live.liveAhead)
	}
}

func TestLiveConfirmThenDeliverShape(t *testing.T) {
	live := newLiveSubscription("run.r1.d", 4, 0, 0, func() {}, nil)
	live.replayDone = true
	ctx := context.Background()
	live.reserve()
	live.confirm(1)
	live.deliverLive(ctx, platform.Message{Subject: "run.r1.d", Seq: 1, Data: []byte("x")})
	if len(live.ch) != 1 {
		t.Fatalf("len=%d", len(live.ch))
	}
}

func TestLiveDeliverThenConfirmShape(t *testing.T) {
	ctx := context.Background()
	msg := platform.Message{Subject: "run.r1.d", Seq: 1, Data: []byte("x")}
	a := newLiveSubscription("run.r1.d", 4, 0, 0, func() {}, nil)
	a.replayDone = true
	a.reserve()
	a.confirm(1)
	a.deliverLive(ctx, msg)
	b := newLiveSubscription("run.r1.d", 4, 0, 0, func() {}, nil)
	b.replayDone = true
	b.reserve()
	b.deliverLive(ctx, msg)
	b.confirm(1)
	if len(a.ch) != len(b.ch) {
		t.Fatalf("len a=%d b=%d", len(a.ch), len(b.ch))
	}
}
