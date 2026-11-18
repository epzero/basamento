// Package bustest provides an in-process platform.Bus and platform.KV fake for
// the BDD gate. See specs/bus/design.md section 1.
package bustest

import (
	"context"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/bus"
)

// Fake implements platform.Bus and platform.KV with the same semantics as the
// JetStream-backed store.
type Fake struct {
	clock   core.Clock
	mu      sync.Mutex
	bufSize int
	maxSize int

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
	id      int
	subject string
	after   uint64
	ch      chan platform.Message
	ctx     context.Context
	cancel  context.CancelFunc
	bufCap  int
	ended   bool
}

var subID int

// NewFake builds a fake bus and KV. Limits follow bus.Options when nonzero.
func NewFake(clock core.Clock, opts bus.Options) *Fake {
	if opts.SubBuffer <= 0 {
		opts.SubBuffer = bus.DefaultSubBuffer
	}
	if opts.MaxMessageSize <= 0 {
		opts.MaxMessageSize = bus.DefaultMaxMessageSize
	}
	return &Fake{
		clock:      clock,
		bufSize:    opts.SubBuffer,
		maxSize:    opts.MaxMessageSize,
		retained:   map[string][]retainedMsg{},
		purgeFloor: map[string]uint64{},
		kv:         map[string]kvEntry{},
	}
}

// SubscribeWithBuffer is for BDD drivers that need a larger channel than the
// configured overflow cap (slow-subscriber scenarios).
func (f *Fake) SubscribeWithBuffer(ctx context.Context, subject string, after uint64, chanBuf, overflowCap int) (<-chan platform.Message, error) {
	return f.subscribe(ctx, subject, after, chanBuf, overflowCap)
}

func (f *Fake) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	f.endAllSubs()
}

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

func (f *Fake) Publish(ctx context.Context, subject string, data []byte) (uint64, error) {
	_ = ctx
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
	for _, s := range f.subs {
		if s.subject == subject && !s.ended {
			f.deliverToSubLocked(s, msg)
		}
	}
	f.mu.Unlock()
	return seq, nil
}

func (f *Fake) deliverToSubLocked(s *sub, msg platform.Message) {
	if s.ended || msg.Seq <= s.after {
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

func (s *sub) endLocked() {
	if s.ended {
		return
	}
	s.ended = true
	s.cancel()
	close(s.ch)
}

func (f *Fake) endAllSubs() {
	for _, s := range f.subs {
		if !s.ended {
			s.endLocked()
		}
	}
}

func (f *Fake) Subscribe(ctx context.Context, subject string, after uint64) (<-chan platform.Message, error) {
	return f.subscribe(ctx, subject, after, f.bufSize, f.bufSize)
}

func (f *Fake) subscribe(ctx context.Context, subject string, after uint64, chanBuf, overflowCap int) (<-chan platform.Message, error) {
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
	subID++
	subCtx, cancel := context.WithCancel(ctx)
	s := &sub{
		id:      subID,
		subject: subject,
		after:   after,
		ch:      make(chan platform.Message, chanBuf),
		ctx:     subCtx,
		cancel:  cancel,
		bufCap:  overflowCap,
	}
	for _, m := range f.retained[subject] {
		if m.seq > after {
			replay := platform.Message{Subject: subject, Seq: m.seq, Data: append([]byte(nil), m.data...)}
			f.deliverToSubLocked(s, replay)
		}
	}
	f.subs = append(f.subs, s)
	f.mu.Unlock()
	go func() {
		<-subCtx.Done()
		f.mu.Lock()
		if !s.ended {
			s.endLocked()
		}
		f.mu.Unlock()
	}()
	return s.ch, nil
}

func (f *Fake) LastSeq(ctx context.Context, subject string) (uint64, error) {
	_ = ctx
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

func (f *Fake) Purge(ctx context.Context, subject string) error {
	_ = ctx
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

func (f *Fake) Get(ctx context.Context, key string) ([]byte, uint64, error) {
	_ = ctx
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
	return append([]byte(nil), e.value...), e.rev, nil
}

func (f *Fake) Put(ctx context.Context, key string, value []byte, ttl time.Duration) (uint64, error) {
	_ = ctx
	if err := f.unavailable(); err != nil {
		return 0, err
	}
	if err := f.checkKey(key); err != nil {
		return 0, err
	}
	if err := f.checkTTL(ttl); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kvRevSeq++
	rev := f.kvRevSeq
	var deadline time.Time
	if ttl > 0 {
		deadline = f.clock.Now().Add(ttl)
	}
	f.kv[key] = kvEntry{value: append([]byte(nil), value...), rev: rev, deadline: deadline}
	return rev, nil
}

func (f *Fake) CompareAndSwap(ctx context.Context, key string, value []byte, revision uint64, ttl time.Duration) (uint64, error) {
	_ = ctx
	if err := f.unavailable(); err != nil {
		return 0, err
	}
	if err := f.checkKey(key); err != nil {
		return 0, err
	}
	if err := f.checkTTL(ttl); err != nil {
		return 0, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var deadline time.Time
	if ttl > 0 {
		deadline = f.clock.Now().Add(ttl)
	}
	if revision == 0 {
		if _, ok := f.kvGetLocked(key); ok {
			return 0, core.NewError(core.CodeConflict, "key exists", nil)
		}
		raw, present := f.kv[key]
		if present && !entryExpired(f.clock, raw) {
			return 0, core.NewError(core.CodeConflict, "key exists", nil)
		}
		if present && entryExpired(f.clock, raw) {
			f.kvRevSeq++
			rev := f.kvRevSeq
			f.kv[key] = kvEntry{value: append([]byte(nil), value...), rev: rev, deadline: deadline}
			return rev, nil
		}
		f.kvRevSeq++
		rev := f.kvRevSeq
		f.kv[key] = kvEntry{value: append([]byte(nil), value...), rev: rev, deadline: deadline}
		return rev, nil
	}
	cur, ok := f.kvGetLocked(key)
	raw, present := f.kv[key]
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
	f.kv[key] = kvEntry{value: append([]byte(nil), value...), rev: rev, deadline: deadline}
	return rev, nil
}

func (f *Fake) Delete(ctx context.Context, key string) error {
	_ = ctx
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
