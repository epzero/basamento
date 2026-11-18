package bus

import (
	"context"
	"sync"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/platform"
)

type stubConsume struct {
	mu      sync.Mutex
	stops   int
	drained int
	closedC chan struct{}
}

func newStubConsume() *stubConsume {
	return &stubConsume{closedC: make(chan struct{}, 1)}
}

func (s *stubConsume) Stop() {
	s.mu.Lock()
	s.stops++
	s.mu.Unlock()
}

func (s *stubConsume) Drain() {
	s.mu.Lock()
	s.drained++
	s.mu.Unlock()
}

func (s *stubConsume) Closed() <-chan struct{} { return s.closedC }

func (s *stubConsume) stopCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stops
}

func TestLiveSubscriptionEndOnce(t *testing.T) {
	for iter := 0; iter < 20; iter++ {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var cancels int
			subCancel := func() {
				cancels++
				cancel()
			}
			live := newLiveSubscription(2, 0, subCancel, nil)
			cc := newStubConsume()
			msg := platform.Message{Subject: "run.r1.d", Seq: 2, Data: []byte("x")}
			live.attach(cc)
			delCtx := context.Background()
			live.deliver(delCtx, msg)
			live.deliver(delCtx, msg)
			live.deliver(delCtx, msg) // overflow (live seq > replayThrough)
			<-ctx.Done()
			live.end()
			live.end()
			if cc.stopCount() != 1 {
				t.Fatalf("stops=%d", cc.stopCount())
			}
			if cancels != 1 {
				t.Fatalf("cancels=%d", cancels)
			}
			for range live.messages() {
			}
		})
	}
}

func TestLiveEndBeforeAttach(t *testing.T) {
	cc := newStubConsume()
	live := newLiveSubscription(2, 0, func() {}, nil)
	live.end()
	live.attach(cc)
	if cc.stopCount() != 1 {
		t.Fatalf("stops=%d", cc.stopCount())
	}
}

var _ jetstream.ConsumeContext = (*stubConsume)(nil)
