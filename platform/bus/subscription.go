package bus

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/platform"
)

type liveSubscription struct {
	ch         chan platform.Message
	bufCap     int
	mu         sync.Mutex
	closed     bool
	cancel     context.CancelFunc
	cancelOnce sync.Once
	stop       sync.Once
	onEnd      func()
	cc         atomic.Pointer[jetstream.ConsumeContext]
}

func newLiveSubscription(buf int, cancel context.CancelFunc, onEnd func()) *liveSubscription {
	return &liveSubscription{
		ch:     make(chan platform.Message, buf),
		bufCap: buf,
		cancel: cancel,
		onEnd:  onEnd,
	}
}

func (s *liveSubscription) setCC(cc jetstream.ConsumeContext) {
	c := cc
	s.cc.Store(&c)
}

func (s *liveSubscription) consumeCC() jetstream.ConsumeContext {
	p := s.cc.Load()
	if p == nil {
		return nil
	}
	return *p
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
		s.fireCancel()
		return
	}
	select {
	case s.ch <- msg:
	default:
		s.endLocked()
		s.fireCancel()
	}
}

func (s *liveSubscription) endLocked() {
	if s.closed {
		return
	}
	s.closed = true
	close(s.ch)
}

func (s *liveSubscription) fireCancel() {
	s.cancelOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
	})
}

func (s *liveSubscription) end(cc jetstream.ConsumeContext) {
	s.stop.Do(func() {
		if cc != nil {
			cc.Stop()
		}
		s.mu.Lock()
		s.endLocked()
		s.mu.Unlock()
		s.fireCancel()
		if s.onEnd != nil {
			s.onEnd()
		}
	})
}

func (s *liveSubscription) messages() <-chan platform.Message { return s.ch }
