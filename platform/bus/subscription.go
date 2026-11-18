package bus

import (
	"context"
	"sync"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/platform"
)

type liveSubscription struct {
	ch     chan platform.Message
	bufCap int
	mu     sync.Mutex
	closed bool
	cancel context.CancelFunc
	stop   sync.Once
}

func newLiveSubscription(buf int, cancel context.CancelFunc) *liveSubscription {
	return &liveSubscription{
		ch:     make(chan platform.Message, buf),
		bufCap: buf,
		cancel: cancel,
	}
}

// deliver attempts a non-blocking send. A full buffer ends the subscription
// after already-buffered messages without delivering msg (REQ-BUS-010).
func (s *liveSubscription) deliver(msg platform.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.ch <- msg:
		return
	default:
	}
	if len(s.ch) >= s.bufCap {
		s.endLocked()
		return
	}
	select {
	case s.ch <- msg:
	default:
		s.endLocked()
	}
}

func (s *liveSubscription) endLocked() {
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

func (s *liveSubscription) end(cc jetstream.ConsumeContext) {
	s.stop.Do(func() {
		if cc != nil {
			cc.Stop()
		}
		s.mu.Lock()
		s.endLocked()
		s.mu.Unlock()
		if s.cancel != nil {
			s.cancel()
		}
	})
}

func (s *liveSubscription) messages() <-chan platform.Message { return s.ch }
