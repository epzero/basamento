package bus

import (
	"context"
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

// replayBlockedFixture fills the buffer from the test goroutine and then starts a
// second replay delivery. With nothing consuming, that sender can only be
// blocked in its send (or not yet scheduled), so the snapshot below always sees
// exactly one delivered message: no spin, no sleep, no hook.
func replayBlockedFixture(t *testing.T, buf int, after, replayThrough uint64) (*liveSubscription, context.Context, *sync.WaitGroup) {
	t.Helper()
	if buf != 1 {
		t.Fatalf("fixture needs a buffer of 1 to keep the second sender blocked, got %d", buf)
	}
	live := newLiveSubscription("run.r1.d", buf, after, replayThrough, func() {}, nil)
	ctx := context.Background()
	live.deliverReplay(ctx, platform.Message{Subject: "run.r1.d", Seq: 1, Data: []byte("a")})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		live.deliverReplay(ctx, platform.Message{Subject: "run.r1.d", Seq: 2, Data: []byte("b")})
	}()
	return live, ctx, &wg
}

// snapBacklogWhileBlocked asserts the state the snapshot relies on before taking
// it: the buffer is full and only the first replay message was delivered.
func snapBacklogWhileBlocked(t *testing.T, live *liveSubscription, snap func()) {
	t.Helper()
	live.mu.Lock()
	full := len(live.ch) == cap(live.ch)
	delivered := live.replayDelivered
	live.mu.Unlock()
	if !full || delivered != 1 {
		t.Fatalf("fixture is not blocked on a full buffer: full=%v delivered=%d", full, delivered)
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
