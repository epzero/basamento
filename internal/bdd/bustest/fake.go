// Package bustest provides an in-process platform.Bus and platform.KV fake for
// the BDD gate (design specs/bus, section 1 — internal/bdd path per architect).
package bustest

import (
	"context"
	"encoding/binary"
	"encoding/json"
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
	lastSeq    map[string]uint64
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
	deadline time.Time // zero — no expiry
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

// NewFake builds a fake bus and KV with bus defaults.
func NewFake(clock core.Clock) *Fake {
	return &Fake{
		clock:      clock,
		bufSize:    bus.DefaultSubBuffer,
		maxSize:    bus.DefaultMaxMessageSize,
		retained:   map[string][]retainedMsg{},
		lastSeq:    map[string]uint64{},
		purgeFloor: map[string]uint64{},
		kv:         map[string]kvEntry{},
	}
}

// SetSubBuffer overrides the subscription buffer (tests only).
func (f *Fake) SetSubBuffer(n int) { f.bufSize = n }

// SetMaxMessageSize overrides the publish size limit (tests only).
func (f *Fake) SetMaxMessageSize(n int) { f.maxSize = n }

// Stop marks the fake unreachable (REQ-BUS-013).
func (f *Fake) Stop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	f.endAllSubs()
}

// Start clears the stopped flag.
func (f *Fake) Start() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = false
}

// Close ends every subscription.
func (f *Fake) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped = true
	f.endAllSubs()
	return nil
}

func (f *Fake) unavailable() error {
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

// Publish implements platform.Bus.
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
	f.retained[subject] = append(f.retained[subject], retainedMsg{seq: seq, data: append([]byte(nil), data...)})
	if seq > f.lastSeq[subject] {
		f.lastSeq[subject] = seq
	}
	msg := platform.Message{Subject: subject, Seq: seq, Data: append([]byte(nil), data...)}
	for _, s := range f.subs {
		if s.subject == subject && !s.ended {
			f.deliverToSub(s, msg)
		}
	}
	f.mu.Unlock()
	return seq, nil
}

func (f *Fake) deliverToSub(s *sub, msg platform.Message) {
	if msg.Seq <= s.after || s.ended {
		return
	}
	select {
	case <-s.ctx.Done():
		return
	default:
	}
	select {
	case <-s.ctx.Done():
		return
	case s.ch <- msg:
		return
	default:
	}
	if len(s.ch) >= s.bufCap {
		s.endLocked()
		return
	}
	select {
	case <-s.ctx.Done():
	case s.ch <- msg:
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

// Subscribe implements platform.Bus.
func (f *Fake) Subscribe(ctx context.Context, subject string, after uint64) (<-chan platform.Message, error) {
	return f.subscribe(ctx, subject, after, f.bufSize, f.bufSize)
}

// SubscribeWithBuffer is for BDD drivers that need a larger channel than the
// configured overflow cap (slow-subscriber scenarios).
func (f *Fake) SubscribeWithBuffer(ctx context.Context, subject string, after uint64, chanBuf, overflowCap int) (<-chan platform.Message, error) {
	return f.subscribe(ctx, subject, after, chanBuf, overflowCap)
}

func (f *Fake) subscribe(ctx context.Context, subject string, after uint64, chanBuf, overflowCap int) (<-chan platform.Message, error) {
	if err := f.unavailable(); err != nil {
		return nil, err
	}
	if err := f.checkSubject(subject); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	floor := f.purgeFloor[subject]
	if after > 0 && after < floor {
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
	f.subs = append(f.subs, s)
	var replay []platform.Message
	for _, m := range f.retained[subject] {
		if m.seq > after {
			replay = append(replay, platform.Message{Subject: subject, Seq: m.seq, Data: append([]byte(nil), m.data...)})
		}
	}
	go func() {
		<-subCtx.Done()
		f.mu.Lock()
		if !s.ended {
			s.endLocked()
		}
		f.mu.Unlock()
	}()
	go func() {
		for _, m := range replay {
			f.deliverToSub(s, m)
		}
	}()
	return s.ch, nil
}

// LastSeq implements platform.Bus.
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
	return f.lastSeq[subject], nil
}

// Purge implements platform.Bus.
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
	msgs := f.retained[subject]
	var max uint64
	for _, m := range msgs {
		if m.seq > max {
			max = m.seq
		}
	}
	if max > f.purgeFloor[subject] {
		f.purgeFloor[subject] = max
	}
	f.retained[subject] = nil
	return nil
}

func (f *Fake) alive() error { return f.unavailable() }

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

// Get implements platform.KV.
func (f *Fake) Get(ctx context.Context, key string) ([]byte, uint64, error) {
	_ = ctx
	if err := f.alive(); err != nil {
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

// Put implements platform.KV.
func (f *Fake) Put(ctx context.Context, key string, value []byte, ttl time.Duration) (uint64, error) {
	_ = ctx
	if err := f.alive(); err != nil {
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

// CompareAndSwap implements platform.KV.
func (f *Fake) CompareAndSwap(ctx context.Context, key string, value []byte, revision uint64, ttl time.Duration) (uint64, error) {
	_ = ctx
	if err := f.alive(); err != nil {
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
	if revision != 0 {
		cur, ok := f.kvGetLocked(key)
		rawStored, physicallyPresent := f.kv[key]
		if !ok {
			if physicallyPresent && rawStored.rev == revision && entryExpired(f.clock, rawStored) {
				return 0, core.NewError(core.CodeConflict, "entry expired", nil)
			}
			return 0, core.NewError(core.CodeConflict, "key absent", nil)
		}
		if cur.rev != revision {
			return 0, core.NewError(core.CodeConflict, "stale revision", nil)
		}
	} else {
		_, ok := f.kvGetLocked(key)
		_, physicallyPresent := f.kv[key]
		if ok {
			return 0, core.NewError(core.CodeConflict, "key exists", nil)
		}
		if physicallyPresent {
			delete(f.kv, key)
		}
	}
	f.kvRevSeq++
	rev := f.kvRevSeq
	var deadline time.Time
	if ttl > 0 {
		deadline = f.clock.Now().Add(ttl)
	}
	f.kv[key] = kvEntry{value: append([]byte(nil), value...), rev: rev, deadline: deadline}
	return rev, nil
}

// Delete implements platform.KV.
func (f *Fake) Delete(ctx context.Context, key string) error {
	_ = ctx
	if err := f.alive(); err != nil {
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

// SnapshotState serializes retained data for embedded restart tests.
func (f *Fake) SnapshotState() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := snapState{
		StreamSeq:  f.streamSeq,
		Retained:   f.retained,
		LastSeq:    f.lastSeq,
		PurgeFloor: f.purgeFloor,
		KV:         f.kv,
		KVRevSeq:   f.kvRevSeq,
		BufSize:    f.bufSize,
		MaxSize:    f.maxSize,
	}
	b, _ := json.Marshal(st)
	return b
}

// RestoreState replaces state from a snapshot.
func (f *Fake) RestoreState(b []byte) error {
	var st snapState
	if err := json.Unmarshal(b, &st); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.endAllSubs()
	f.streamSeq = st.StreamSeq
	f.retained = st.Retained
	f.lastSeq = st.LastSeq
	f.purgeFloor = st.PurgeFloor
	f.kv = st.KV
	f.kvRevSeq = st.KVRevSeq
	if st.BufSize > 0 {
		f.bufSize = st.BufSize
	}
	if st.MaxSize > 0 {
		f.maxSize = st.MaxSize
	}
	return nil
}

type snapState struct {
	StreamSeq  uint64
	Retained   map[string][]retainedMsg
	LastSeq    map[string]uint64
	PurgeFloor map[string]uint64
	KV         map[string]kvEntry
	KVRevSeq   uint64
	BufSize    int
	MaxSize    int
}

// EncodeKVValue packs value and deadline for persistence tests.
func EncodeKVValue(value []byte, deadline time.Time) []byte {
	var ns int64
	if !deadline.IsZero() {
		ns = deadline.UnixNano()
	}
	buf := make([]byte, 8+len(value))
	binary.BigEndian.PutUint64(buf[:8], uint64(ns))
	copy(buf[8:], value)
	return buf
}
