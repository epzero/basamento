// Package bustest provides an in-process platform.Bus and platform.KV fake for
// the BDD gate. See specs/bus/design.md section 1.
package bustest

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/bus"
)

// Fake implements platform.Bus and platform.KV with the same semantics as the
// JetStream-backed store.
type Fake struct {
	clock        core.Clock
	mu           sync.Mutex
	pubMu        sync.Mutex
	bufSize      int
	maxSize      int
	kvValueLimit int

	nextReplayGate chan struct{}

	stopped bool

	streamSeq  uint64
	retained   map[string][]retainedMsg
	purgeFloor map[string]uint64

	kv       map[string]kvEntry
	kvRevSeq uint64

	subs []*sub
}

type retainedMsg struct {
	seq  uint64
	data []byte
}

type kvEntry struct {
	value    []byte
	rev      uint64
	deadline time.Time
}

type sub struct {
	subject           string
	after             uint64
	replayThrough     uint64
	replayPending     int
	liveAhead         int
	dropFrom          uint64
	dropLive          bool
	ch                chan platform.Message
	cancel            context.CancelFunc
	replayActive      bool
	livePending       []platform.Message
	liveWake          chan struct{}
	ended             bool
	chClosed          bool
	senders           sync.WaitGroup
	done              chan struct{}
	teardownOnce      sync.Once
	replaySettled     chan struct{}
	replaySettledOnce sync.Once
	replayGate        chan struct{}
}

// NewFake builds a fake bus and KV. Limits follow bus.Options when nonzero.
func NewFake(clock core.Clock, opts bus.Options) *Fake {
	if opts.SubBuffer <= 0 {
		opts.SubBuffer = bus.DefaultSubBuffer
	}
	if opts.MaxMessageSize <= 0 {
		opts.MaxMessageSize = bus.DefaultMaxMessageSize
	}
	lim := bus.LimitsFor(opts.MaxMessageSize, int(bus.EmbeddedServerMaxPayload(opts.MaxMessageSize)))
	return &Fake{
		clock:        clock,
		bufSize:      opts.SubBuffer,
		maxSize:      lim.MaxPublish,
		kvValueLimit: lim.MaxKVValue,
		retained:     map[string][]retainedMsg{},
		purgeFloor:   map[string]uint64{},
		kv:           map[string]kvEntry{},
	}
}

// Stop simulates bus unavailability without destroying state.
func (f *Fake) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	f.endAllSubs()
}

// Close stops the fake and ends every subscription.
func (f *Fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	f.endAllSubs()
	return nil
}

func (f *Fake) unavailable() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stopped {
		return core.NewError(core.CodeUnavailable, "bus unavailable", nil)
	}
	return nil
}

func (f *Fake) checkSubject(subject string) error {
	if !bus.ValidSubject(subject) {
		return core.NewError(core.CodeInvalidArgument, "malformed subject", nil)
	}
	return nil
}

func (f *Fake) checkKey(key string) error {
	if !bus.ValidKey(key) {
		return core.NewError(core.CodeInvalidArgument, "malformed key", nil)
	}
	return nil
}

func (f *Fake) checkTTL(ttl time.Duration) error {
	if ttl < 0 {
		return core.NewError(core.CodeInvalidArgument, "negative ttl", nil)
	}
	return nil
}

func (f *Fake) checkKVValue(value []byte) error {
	if len(value) > f.kvValueLimit {
		return core.NewError(core.CodeInvalidArgument, "kv value too large", nil)
	}
	return nil
}

func (f *Fake) lastSeqLocked(subject string) uint64 {
	floor := f.purgeFloor[subject]
	var max uint64
	for _, m := range f.retained[subject] {
		if m.seq > max {
			max = m.seq
		}
	}
	if max > floor {
		return max
	}
	return floor
}

func (f *Fake) terminateSub(s *sub) {
	s.teardownOnce.Do(func() {
		f.mu.Lock()
		s.ended = true
		close(s.done)
		f.mu.Unlock()
		s.senders.Wait()
		f.mu.Lock()
		if !s.chClosed {
			s.chClosed = true
			close(s.ch)
		}
		f.removeSub(s)
		f.mu.Unlock()
		s.cancelOnce()
	})
}

func (s *sub) cancelOnce() {
	if s.cancel != nil {
		s.cancel()
	}
}

func (f *Fake) replaySend(subCtx context.Context, s *sub, msg platform.Message) {
	f.mu.Lock()
	if s.ended || s.chClosed {
		f.mu.Unlock()
		return
	}
	s.senders.Add(1)
	f.mu.Unlock()
	defer s.senders.Done()
	select {
	case s.ch <- msg:
		f.mu.Lock()
		if s.replayPending > 0 {
			s.replayPending--
		}
		f.mu.Unlock()
	case <-subCtx.Done():
	case <-s.done:
	}
}

func (f *Fake) flushLivePending(s *sub, subCtx context.Context) (ended bool) {
	for {
		f.mu.Lock()
		pending := s.livePending
		s.livePending = nil
		f.mu.Unlock()
		for _, msg := range pending {
			if msg.Seq <= s.after {
				continue
			}
			f.mu.Lock()
			closed := s.chClosed
			stopped := s.ended
			f.mu.Unlock()
			if stopped || closed {
				return true
			}
			if msg.Seq > s.replayThrough {
				f.mu.Lock()
				if s.liveAhead > 0 {
					s.liveAhead--
				}
				if s.replayPending > 0 {
					s.replayPending = 0
				}
				f.mu.Unlock()
			}
			f.mu.Lock()
			replayLeft := s.replayActive || s.replayPending > 0
			f.mu.Unlock()
			if replayLeft {
				f.replaySend(subCtx, s, msg)
				continue
			}
			f.mu.Lock()
			overflow := f.deliverLiveLocked(s, msg)
			f.mu.Unlock()
			if overflow {
				f.terminateSub(s)
				return true
			}
		}
		f.mu.Lock()
		if len(s.livePending) == 0 {
			f.mu.Unlock()
			return false
		}
		f.mu.Unlock()
	}
}

func (f *Fake) callCtx(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return core.NewError(core.CodeTimeout, "timeout", nil)
		}
		return core.NewError(core.CodeCancelled, "cancelled", nil)
	}
	return nil
}

// Publish stores a message and delivers it to active subscribers of the subject.
func (f *Fake) Publish(ctx context.Context, subject string, data []byte) (uint64, error) {
	if err := f.callCtx(ctx); err != nil {
		return 0, err
	}
	f.pubMu.Lock()
	defer f.pubMu.Unlock()
	if err := f.unavailable(); err != nil {
		return 0, err
	}
	if err := f.checkSubject(subject); err != nil {
		return 0, err
	}
	if len(data) > f.maxSize {
		return 0, core.NewError(core.CodeInvalidArgument, "message too large", nil)
	}
	f.mu.Lock()
	f.streamSeq++
	seq := f.streamSeq
	msg := platform.Message{Subject: subject, Seq: seq, Data: append([]byte(nil), data...)}
	f.retained[subject] = append(f.retained[subject], retainedMsg{seq: seq, data: msg.Data})
	snap := append([]*sub(nil), f.subs...)
	for _, s := range snap {
		if s.subject != subject || s.ended {
			continue
		}
		// The fake knows the sequence before delivery, so it reserves only for a
		// message the subscription will actually deliver live. The store cannot
		// know the sequence until the publish is acked: it reserves on every
		// publish and confirms (or releases) at the ack. Same state, two shapes:
		// do not "harmonize" them without keeping the below-after release.
		if msg.Seq > s.replayThrough && msg.Seq > s.after {
			s.reserve()
		}
	}
	var terminate []*sub
	for _, s := range snap {
		if s.subject != subject {
			continue
		}
		if s.ended {
			continue
		}
		if msg.Seq > s.replayThrough {
			s.confirm(msg.Seq)
			if s.replayActive {
				s.livePending = append(s.livePending, msg)
				select {
				case s.liveWake <- struct{}{}:
				default:
				}
				continue
			}
		}
		if s.replayActive {
			s.livePending = append(s.livePending, msg)
			select {
			case s.liveWake <- struct{}{}:
			default:
			}
			continue
		}
		if f.deliverLiveLocked(s, msg) {
			terminate = append(terminate, s)
		}
	}
	f.mu.Unlock()
	for _, s := range terminate {
		f.terminateSub(s)
	}
	return seq, nil
}

func (sub *sub) bufferOccupied() int {
	return len(sub.ch) + sub.replayPending + sub.liveAhead
}

func (s *sub) reserve() {
	if s.ended {
		return
	}
	s.liveAhead++
}

// confirm mirrors liveSubscription.confirm: a message at or below the replay
// bound is replayed, one at or below after is skipped, and neither counts
// toward live overflow (REQ-BUS-004).
func (s *sub) confirm(seq uint64) {
	if s.ended {
		return
	}
	if seq <= s.replayThrough || seq <= s.after {
		if s.liveAhead > 0 {
			s.liveAhead--
		}
		return
	}
	if s.bufferOccupied() > cap(s.ch) && !s.dropLive {
		s.dropLive = true
		s.dropFrom = seq
	}
}

func (f *Fake) deliverLiveLocked(s *sub, msg platform.Message) bool {
	if s.ended || msg.Seq <= s.after {
		return false
	}
	if s.liveAhead > 0 {
		s.liveAhead--
	}
	if s.dropLive {
		if s.dropFrom > 0 && msg.Seq >= s.dropFrom {
			return true
		}
	}
	select {
	case s.ch <- msg:
		return false
	default:
		return true
	}
}

func (f *Fake) removeSub(s *sub) {
	for i, x := range f.subs {
		if x == s {
			f.subs = append(f.subs[:i], f.subs[i+1:]...)
			return
		}
	}
}

func (f *Fake) endAllSubs() {
	for len(f.subs) > 0 {
		s := f.subs[0]
		f.mu.Unlock()
		f.terminateSub(s)
		f.mu.Lock()
	}
}

// Subscribe opens a subject subscription after the given bus sequence.
func (f *Fake) Subscribe(ctx context.Context, subject string, after uint64) (<-chan platform.Message, error) {
	if err := f.callCtx(ctx); err != nil {
		return nil, err
	}
	if err := f.unavailable(); err != nil {
		return nil, err
	}
	if err := f.checkSubject(subject); err != nil {
		return nil, err
	}
	f.mu.Lock()
	floor := f.purgeFloor[subject]
	if after > 0 && after < floor {
		f.mu.Unlock()
		return nil, core.NewError(core.CodeNotFound, "purged", map[string]any{"purged_through": floor})
	}
	replayThrough := f.lastSeqLocked(subject)
	replayGate := f.nextReplayGate
	f.nextReplayGate = nil
	subCtx, cancel := context.WithCancel(ctx)
	s := &sub{
		subject:       subject,
		after:         after,
		replayThrough: replayThrough,
		ch:            make(chan platform.Message, f.bufSize),
		cancel:        cancel,
		done:          make(chan struct{}),
		liveWake:      make(chan struct{}, 1),
		replaySettled: make(chan struct{}),
		replayGate:    replayGate,
	}
	var replay []retainedMsg
	for _, m := range f.retained[subject] {
		if m.seq > after {
			replay = append(replay, m)
		}
	}
	for _, m := range replay {
		if m.seq <= replayThrough {
			s.replayPending++
		}
	}
	if len(replay) > 0 {
		s.replayActive = true
	}
	f.subs = append(f.subs, s)
	f.mu.Unlock()
	go func() {
		defer func() {
			<-subCtx.Done()
			f.terminateSub(s)
		}()
		if replayGate != nil {
			<-replayGate
		}
		for _, m := range replay {
			f.mu.Lock()
			ended := s.ended
			f.mu.Unlock()
			if ended {
				return
			}
			msg := platform.Message{Subject: subject, Seq: m.seq, Data: append([]byte(nil), m.data...)}
			if m.seq <= replayThrough {
				f.mu.Lock()
				closed := s.chClosed
				f.mu.Unlock()
				if closed {
					return
				}
				f.replaySend(subCtx, s, msg)
			}
		}
		s.replaySettledOnce.Do(func() { close(s.replaySettled) })
		f.mu.Lock()
		s.replayActive = false
		f.mu.Unlock()
		for {
			if f.flushLivePending(s, subCtx) {
				return
			}
			f.mu.Lock()
			if s.ended {
				f.mu.Unlock()
				return
			}
			if len(s.livePending) > 0 {
				f.mu.Unlock()
				continue
			}
			f.mu.Unlock()
			select {
			case <-s.liveWake:
			case <-subCtx.Done():
				return
			case <-s.done:
				return
			}
		}
	}()
	return s.ch, nil
}

// LastSeq returns the highest bus sequence assigned on the subject.
func (f *Fake) LastSeq(ctx context.Context, subject string) (uint64, error) {
	if err := f.callCtx(ctx); err != nil {
		return 0, err
	}
	if err := f.unavailable(); err != nil {
		return 0, err
	}
	if err := f.checkSubject(subject); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSeqLocked(subject), nil
}

// Purge drops retained messages up to the current last sequence on the subject.
func (f *Fake) Purge(ctx context.Context, subject string) error {
	if err := f.callCtx(ctx); err != nil {
		return err
	}
	if err := f.unavailable(); err != nil {
		return err
	}
	if err := f.checkSubject(subject); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	L := f.lastSeqLocked(subject)
	if L == 0 {
		return nil
	}
	if L > f.purgeFloor[subject] {
		f.purgeFloor[subject] = L
	}
	var kept []retainedMsg
	for _, m := range f.retained[subject] {
		if m.seq > L {
			kept = append(kept, m)
		}
	}
	f.retained[subject] = kept
	return nil
}

func (f *Fake) kvGetLocked(key string) (kvEntry, bool) {
	e, ok := f.kv[key]
	if !ok {
		return kvEntry{}, false
	}
	if entryExpired(f.clock, e) {
		return kvEntry{}, false
	}
	return e, true
}

func entryExpired(clock core.Clock, e kvEntry) bool {
	return !e.deadline.IsZero() && !clock.Now().Before(e.deadline)
}

// Get reads the latest value and revision of a KV key.
func (f *Fake) Get(ctx context.Context, key string) ([]byte, uint64, error) {
	if err := f.callCtx(ctx); err != nil {
		return nil, 0, err
	}
	if err := f.unavailable(); err != nil {
		return nil, 0, err
	}
	if err := f.checkKey(key); err != nil {
		return nil, 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.kvGetLocked(key)
	if !ok {
		return nil, 0, core.NewError(core.CodeNotFound, "key absent", nil)
	}
	val, err := bus.DecodeKVBucketValue(e.value)
	if err != nil {
		return nil, 0, err
	}
	return append([]byte(nil), val...), e.rev, nil
}

// Put stores or overwrites a KV entry.
func (f *Fake) Put(ctx context.Context, key string, value []byte, ttl time.Duration) (uint64, error) {
	if err := f.callCtx(ctx); err != nil {
		return 0, err
	}
	if err := f.unavailable(); err != nil {
		return 0, err
	}
	if err := f.checkKey(key); err != nil {
		return 0, err
	}
	if err := f.checkTTL(ttl); err != nil {
		return 0, err
	}
	if err := f.checkKVValue(value); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kvRevSeq++
	rev := f.kvRevSeq
	deadline := bus.KVDeadlineFromTTL(f.clock.Now(), ttl)
	wire := bus.EncodeKVBucketsValue(append([]byte(nil), value...), deadline)
	f.kv[key] = kvEntry{value: wire, rev: rev, deadline: deadline}
	return rev, nil
}

// CompareAndSwap updates a key when revision is zero (create) or matches.
func (f *Fake) CompareAndSwap(ctx context.Context, key string, value []byte, revision uint64, ttl time.Duration) (uint64, error) {
	if err := f.callCtx(ctx); err != nil {
		return 0, err
	}
	if err := f.unavailable(); err != nil {
		return 0, err
	}
	if err := f.checkKey(key); err != nil {
		return 0, err
	}
	if err := f.checkTTL(ttl); err != nil {
		return 0, err
	}
	if err := f.checkKVValue(value); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	deadline := bus.KVDeadlineFromTTL(f.clock.Now(), ttl)
	if revision == 0 {
		raw, present := f.kv[key]
		if present && !entryExpired(f.clock, raw) {
			if _, err := bus.DecodeKVBucketValue(raw.value); err != nil {
				return 0, err
			}
			return 0, core.NewError(core.CodeConflict, "key exists", nil)
		}
		if present && entryExpired(f.clock, raw) {
			f.kvRevSeq++
			rev := f.kvRevSeq
			wire := bus.EncodeKVBucketsValue(append([]byte(nil), value...), deadline)
			f.kv[key] = kvEntry{value: wire, rev: rev, deadline: deadline}
			return rev, nil
		}
		f.kvRevSeq++
		rev := f.kvRevSeq
		wire := bus.EncodeKVBucketsValue(append([]byte(nil), value...), deadline)
		f.kv[key] = kvEntry{value: wire, rev: rev, deadline: deadline}
		return rev, nil
	}
	cur, ok := f.kvGetLocked(key)
	raw, present := f.kv[key]
	if present {
		if _, err := bus.DecodeKVBucketValue(raw.value); err != nil {
			return 0, err
		}
	}
	if !ok {
		if present && raw.rev == revision && entryExpired(f.clock, raw) {
			return 0, core.NewError(core.CodeConflict, "entry expired", nil)
		}
		return 0, core.NewError(core.CodeConflict, "key absent", nil)
	}
	if cur.rev != revision {
		return 0, core.NewError(core.CodeConflict, "stale revision", nil)
	}
	f.kvRevSeq++
	rev := f.kvRevSeq
	wire := bus.EncodeKVBucketsValue(append([]byte(nil), value...), deadline)
	f.kv[key] = kvEntry{value: wire, rev: rev, deadline: deadline}
	return rev, nil
}

// Delete removes a KV key.
func (f *Fake) Delete(ctx context.Context, key string) error {
	if err := f.callCtx(ctx); err != nil {
		return err
	}
	if err := f.unavailable(); err != nil {
		return err
	}
	if err := f.checkKey(key); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.kv, key)
	return nil
}
