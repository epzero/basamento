package bus

import (
	"context"
	"sync"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/platform"
)

type liveSubscription struct {
	subject         string
	ch              chan platform.Message
	after           uint64
	replayThrough   uint64
	replayPending   int
	replayDelivered int
	replayDone      bool
	liveAhead       int
	dropFrom        uint64
	dropLive        bool
	mu              sync.Mutex
	ended           bool
	senders         sync.WaitGroup
	done            chan struct{}
	cc              jetstream.ConsumeContext
	cancel          context.CancelFunc
	onEnd           func()
	cancelOnce      sync.Once
}

func newLiveSubscription(subject string, buf int, after, replayThrough uint64, cancel context.CancelFunc, onEnd func()) *liveSubscription {
	replayDone := replayThrough <= after
	return &liveSubscription{
		subject:       subject,
		after:         after,
		ch:            make(chan platform.Message, buf),
		replayThrough: replayThrough,
		replayDone:    replayDone,
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

// setReplayBacklog sets replayPending from the consumer's retained backlog at Subscribe.
func (s *liveSubscription) setReplayBacklog(numPending uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replayDone {
		return
	}
	dist := s.replayThrough - s.after
	n := numPending
	if n > dist {
		n = dist
	}
	rp := int(n)
	if rp < s.replayDelivered {
		rp = 0
	} else {
		rp -= s.replayDelivered
	}
	s.replayPending = rp
}

// setReplayBacklogDistance is the fallback when OrderedConsumer CachedInfo is nil
// (normal Subscribe reads CachedInfo after Consume and does not call this).
func (s *liveSubscription) setReplayBacklogDistance() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replayDone {
		return
	}
	var dist uint64
	if s.replayThrough > s.after {
		dist = s.replayThrough - s.after
		if dist > uint64(^uint(0)>>1) {
			s.replayPending = int(^uint(0) >> 1)
			return
		}
	}
	rp := int(dist)
	if rp < s.replayDelivered {
		rp = 0
	} else {
		rp -= s.replayDelivered
	}
	s.replayPending = rp
}

func (s *liveSubscription) markReplayDoneLocked() {
	if s.replayDone {
		return
	}
	s.replayDone = true
	s.replayPending = 0
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
		s.mu.Lock()
		s.replayDelivered++
		if s.replayPending > 0 {
			s.replayPending--
		}
		if msg.Seq >= s.replayThrough {
			s.markReplayDoneLocked()
		}
		s.mu.Unlock()
	case <-ctx.Done():
	case <-s.done:
	}
}

// deliverLive ends the subscription on overflow (dropFrom or full buffer), never blocking the publisher.
func (s *liveSubscription) deliverLive(ctx context.Context, msg platform.Message) {
	if ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	if s.ended {
		s.mu.Unlock()
		return
	}
	if s.liveAhead > 0 {
		s.liveAhead--
	}
	s.markReplayDoneLocked()
	if s.dropLive && s.dropFrom > 0 && msg.Seq >= s.dropFrom {
		s.mu.Unlock()
		s.terminate()
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

func (s *liveSubscription) reserve() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.liveAhead++
}

// confirm reconciles a reservation with the sequence the publish received.
// A message at or below the replay bound is delivered by the replay path, and a
// message at or below after is below the consumer's start (after+1) and skipped
// entirely; neither is delivered live, so neither may count toward live
// overflow. Their reservation is released here instead (REQ-BUS-004).
func (s *liveSubscription) confirm(seq uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	if seq <= s.replayThrough || seq <= s.after {
		if s.liveAhead > 0 {
			s.liveAhead--
		}
		return
	}
	if s.occupiedLocked() > cap(s.ch) && !s.dropLive {
		s.dropLive = true
		s.dropFrom = seq
	}
}

func (s *liveSubscription) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	if s.liveAhead > 0 {
		s.liveAhead--
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

func (s *liveSubscription) occupiedLocked() int {
	return len(s.ch) + s.replayPending + s.liveAhead
}
