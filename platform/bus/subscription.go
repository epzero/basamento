package bus

import (
	"context"
	"sync"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/platform"
)

type liveSubscription struct {
	ch         chan platform.Message
	bufCap     int
	mu         sync.Mutex
	ended      bool
	cc         jetstream.ConsumeContext
	cancel     context.CancelFunc
	onEnd      func()
	cancelOnce sync.Once
}

func newLiveSubscription(buf int, cancel context.CancelFunc, onEnd func()) *liveSubscription {
	return &liveSubscription{
		ch:     make(chan platform.Message, buf),
		bufCap: buf,
		cancel: cancel,
		onEnd:  onEnd,
	}
}

func (s *liveSubscription) attach(cc jetstream.ConsumeContext) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		if cc != nil {
			cc.Stop()
		}
		return
	}
	s.cc = cc
}

// deliver attempts a non-blocking send. A full buffer ends the subscription
// after already-buffered messages without delivering msg (REQ-BUS-010).
func (s *liveSubscription) deliver(msg platform.Message) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	select {
	case s.ch <- msg:
		s.mu.Unlock()
	default:
		s.ended = true
		cc := s.cc
		close(s.ch)
		s.mu.Unlock()
		s.finish(cc)
	}
}

func (s *liveSubscription) end() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	cc := s.cc
	close(s.ch)
	s.mu.Unlock()
	s.finish(cc)
}

func (s *liveSubscription) finish(cc jetstream.ConsumeContext) {
	if cc != nil {
		cc.Stop()
	}
	s.cancelOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.onEnd != nil {
			s.onEnd()
		}
	})
}

func (s *liveSubscription) messages() <-chan platform.Message { return s.ch }
