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
	const n = 3
	for iter := 0; iter < n; iter++ {
		t.Run("", func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var cancels int
			subCancel := func() {
				cancels++
				cancel()
			}
			live := newLiveSubscription(2, subCancel, nil)
			cc := newStubConsume()
			msg := platform.Message{Subject: "run.r1.d", Seq: 1, Data: []byte("x")}
			live.deliver(msg)
			live.deliver(msg)
			live.deliver(msg) // overflow
			<-ctx.Done()
			live.end(cc)
			live.end(cc)
			if cc.stopCount() != 1 {
				t.Fatalf("stops=%d", cc.stopCount())
			}
			if cancels != 1 {
				t.Fatalf("cancels=%d", cancels)
			}
			ch := live.messages()
			for {
				_, ok := <-ch
				if !ok {
					break
				}
			}
		})
	}
}

var _ jetstream.ConsumeContext = (*stubConsume)(nil)
