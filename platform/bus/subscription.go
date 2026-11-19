package bus

import (
	"context"
	"sync"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/platform"
)

type liveSubscription struct {
	ch            chan platform.Message
	replayThrough uint64
	mu            sync.Mutex
	ended         bool
	senders       sync.WaitGroup
	done          chan struct{}
	cc            jetstream.ConsumeContext
	cancel        context.CancelFunc
	onEnd         func()
	cancelOnce    sync.Once
}

func newLiveSubscription(buf int, replayThrough uint64, cancel context.CancelFunc, onEnd func()) *liveSubscription {
	return &liveSubscription{
		ch:            make(chan platform.Message, buf),
		replayThrough: replayThrough,
		done:          make(chan struct{}),
		cancel:        cancel,
		onEnd:         onEnd,
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

// deliver routes replay with back-pressure and live without blocking the publisher.
func (s *liveSubscription) deliver(ctx context.Context, msg platform.Message) {
	if msg.Seq > 0 && msg.Seq <= s.replayThrough {
		s.deliverReplay(ctx, msg)
		return
	}
	s.deliverLive(ctx, msg)
}

func (s *liveSubscription) deliverReplay(ctx context.Context, msg platform.Message) {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.senders.Add(1)
	s.mu.Unlock()
	defer s.senders.Done()
	select {
	case s.ch <- msg:
	case <-ctx.Done():
	case <-s.done:
	}
}

// deliverLive drops the message when ctx is already done, then non-blocks or ends on overflow.
func (s *liveSubscription) deliverLive(ctx context.Context, msg platform.Message) {
	if ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	select {
	case s.ch <- msg:
		s.mu.Unlock()
	default:
		s.mu.Unlock()
		s.terminate()
	}
}

func (s *liveSubscription) end() {
	s.terminate()
}

func (s *liveSubscription) terminate() {
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	s.ended = true
	close(s.done)
	cc := s.cc
	s.mu.Unlock()
	s.senders.Wait()
	close(s.ch)
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
