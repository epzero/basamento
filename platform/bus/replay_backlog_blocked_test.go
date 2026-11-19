package bus

import (
	"context"
	"runtime"
	"sync"
	"testing"

	"github.com/epzero/basamento/platform"
)

func TestReplayBacklogDuringBlockedReplayDeliver(t *testing.T) {
	const buf, pending = 1, 5
	live, _, wg := replayBlockedFixture(t, buf, 0, 10)
	snapBacklogWhileBlocked(t, live, func() { live.setReplayBacklog(pending) })
	finishBlockedReplayAssert(t, live, wg, pending-2)
	assertLiveOverflowOnConfirm(t, live)
}

func TestReplayBacklogDistanceDuringBlockedReplayDeliver(t *testing.T) {
	const buf = 1
	live, _, wg := replayBlockedFixture(t, buf, 0, 5)
	snapBacklogWhileBlocked(t, live, func() { live.setReplayBacklogDistance() })
	finishBlockedReplayAssert(t, live, wg, 3)
}

func replayBlockedFixture(t *testing.T, buf int, after, replayThrough uint64) (*liveSubscription, context.Context, *sync.WaitGroup) {
	t.Helper()
	live := newLiveSubscription("run.r1.d", buf, after, replayThrough, func() {}, nil)
	ctx := context.Background()
	live.deliverReplay(ctx, platform.Message{Subject: "run.r1.d", Seq: 1, Data: []byte("a")})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		live.deliverReplay(ctx, platform.Message{Subject: "run.r1.d", Seq: 2, Data: []byte("b")})
	}()
	waitChannelFull(t, live)
	return live, ctx, &wg
}

func waitChannelFull(t *testing.T, live *liveSubscription) {
	t.Helper()
	for i := 0; i < 10000; i++ {
		live.mu.Lock()
		full := len(live.ch) == cap(live.ch)
		live.mu.Unlock()
		if full {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("channel not full while replay goroutine should be blocked")
}

func snapBacklogWhileBlocked(t *testing.T, live *liveSubscription, snap func()) {
	t.Helper()
	waitChannelFull(t, live)
	for i := 0; i < 500; i++ {
		runtime.Gosched()
	}
	snap()
}

func finishBlockedReplayAssert(t *testing.T, live *liveSubscription, wg *sync.WaitGroup, wantPending int) {
	t.Helper()
	<-live.messages()
	wg.Wait()
	live.mu.Lock()
	rp := live.replayPending
	live.mu.Unlock()
	if rp != wantPending {
		t.Fatalf("replayPending=%d want %d", rp, wantPending)
	}
}

func assertLiveOverflowOnConfirm(t *testing.T, live *liveSubscription) {
	t.Helper()
	live.reserve()
	live.confirm(20)
	live.mu.Lock()
	drop := live.dropLive
	from := live.dropFrom
	live.mu.Unlock()
	if !drop || from != 20 {
		t.Fatalf("drop=%v from=%d want overflow on seq 20", drop, from)
	}
}
