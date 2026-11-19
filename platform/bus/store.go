package bus

import (
	"context"
	"encoding/binary"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

const (
	streamName = "KIT_BUS"
	metaBucket = "KIT_BUS_META"
	dataBucket = "KIT_KV"
	metaPF     = "pf."
)

// Store implements platform.Bus and platform.KV over one JetStream deployment.
type Store struct {
	clock        core.Clock
	nc           *nats.Conn
	js           jetstream.JetStream
	stream       jetstream.Stream
	meta         jetstream.KeyValue
	data         jetstream.KeyValue
	subBuf       int
	publishLimit int

	closeSrv  func()
	inprocess *server.Server
	stopOnce  sync.Once

	closed atomic.Bool
	liveMu sync.Mutex
	live   map[*liveSubscription]struct{}
}

// Options configures a Store. Zero values pick defaults via withDefaults.
type Options struct {
	// Clock decides KV expiry (REQ-BUS-018). System clock in production; fake in tests.
	Clock core.Clock
	// SubBuffer is the undelivered message cap per subscription (REQ-BUS-010). Default 256.
	SubBuffer int
	// MaxMessageSize is the largest publish payload accepted (REQ-BUS-011). Default 1 MiB.
	MaxMessageSize int
	// Replicas is the JetStream replica count when this open creates streams or KV buckets. Default 1; cloud only when creating.
	Replicas int
}

func (o Options) withDefaults() Options {
	if o.Clock == nil {
		o.Clock = core.SystemClock{}
	}
	if o.SubBuffer <= 0 {
		o.SubBuffer = DefaultSubBuffer
	}
	if o.MaxMessageSize <= 0 {
		o.MaxMessageSize = DefaultMaxMessageSize
	}
	if o.Replicas <= 0 {
		o.Replicas = 1
	}
	return o
}

func checkCallCtx(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return mapConnErr(err)
	}
	return nil
}

func connectAndOpen(ctx context.Context, nc *nats.Conn, opts Options, closeSrv func()) (*Store, error) {
	if err := checkCallCtx(ctx); err != nil {
		return nil, err
	}
	opts = opts.withDefaults()
	if err := nc.FlushTimeout(2 * time.Second); err != nil {
		nc.Close()
		return nil, unavailable()
	}
	if nc.Status() != nats.CONNECTED {
		nc.Close()
		return nil, unavailable()
	}
	if mp := nc.MaxPayload(); mp > 0 {
		need := MinClusterMaxPayload(opts.MaxMessageSize)
		if int(mp) < need {
			nc.Close()
			return nil, clusterPayloadTooSmall()
		}
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, mapConnErr(err)
	}
	assets, err := openJetAssets(ctx, js, opts)
	if err != nil {
		nc.Close()
		return nil, err
	}
	limit := opts.MaxMessageSize
	if closeSrv == nil {
		closeSrv = func() {}
	}
	return &Store{
		clock:        opts.Clock,
		nc:           nc,
		js:           js,
		stream:       assets.stream,
		meta:         assets.meta,
		data:         assets.data,
		subBuf:       opts.SubBuffer,
		publishLimit: limit,
		closeSrv:     closeSrv,
	}, nil
}

// Close ends live subscriptions, releases the client connection and, for embedded
// servers, stops the in-process NATS.
func (s *Store) Close() error {
	s.stopOnce.Do(func() {
		s.closed.Store(true)
		s.liveMu.Lock()
		subs := make([]*liveSubscription, 0, len(s.live))
		for l := range s.live {
			subs = append(subs, l)
		}
		s.liveMu.Unlock()
		for _, l := range subs {
			l.end()
		}
		if s.nc != nil {
			s.nc.Close()
		}
		if s.closeSrv != nil {
			s.closeSrv()
		}
	})
	return nil
}

func (s *Store) tryRegisterLive(l *liveSubscription) error {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if s.closed.Load() {
		return unavailable()
	}
	if s.live == nil {
		s.live = map[*liveSubscription]struct{}{}
	}
	s.live[l] = struct{}{}
	return nil
}

func (s *Store) unregisterLive(l *liveSubscription) {
	s.liveMu.Lock()
	delete(s.live, l)
	s.liveMu.Unlock()
}

// ForceDisconnect closes the client connection while keeping store state (unreachable server).
func (s *Store) ForceDisconnect() {
	if s.nc != nil {
		s.nc.Close()
	}
}

func (s *Store) ready() error {
	if s.closed.Load() {
		return unavailable()
	}
	if s.nc == nil || s.nc.Status() != nats.CONNECTED {
		return unavailable()
	}
	return nil
}

func metaKey(prefix, subject string) string { return prefix + subject }

func uint64Bytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// InProcessServer returns the embedded NATS server for in-process clients, or nil on cloud.
func (s *Store) InProcessServer() *server.Server {
	return s.inprocess
}

// Publish implements platform.Bus.
func (s *Store) Publish(ctx context.Context, subject string, data []byte) (uint64, error) {
	if err := checkCallCtx(ctx); err != nil {
		return 0, err
	}
	if err := s.ready(); err != nil {
		return 0, err
	}
	if !ValidSubject(subject) {
		return 0, invalidSubject()
	}
	if len(data) > s.publishLimit {
		return 0, messageTooLarge()
	}
	ack, err := s.js.Publish(ctx, wireSubject(subject), data)
	if err != nil {
		return 0, mapConnErr(err)
	}
	s.noteLivePublish(subject, ack.Sequence)
	return ack.Sequence, nil
}

func (s *Store) noteLivePublish(subject string, seq uint64) {
	s.liveMu.Lock()
	subs := make([]*liveSubscription, 0, len(s.live))
	for l := range s.live {
		if l.subject == subject {
			subs = append(subs, l)
		}
	}
	s.liveMu.Unlock()
	for _, l := range subs {
		l.noteLivePublish(seq)
	}
}

// LastSeq implements platform.Bus.
func (s *Store) LastSeq(ctx context.Context, subject string) (uint64, error) {
	if err := checkCallCtx(ctx); err != nil {
		return 0, err
	}
	if err := s.ready(); err != nil {
		return 0, err
	}
	if !ValidSubject(subject) {
		return 0, invalidSubject()
	}
	return lastSeqForSubject(ctx, s.stream, s.meta, subject)
}

// Purge implements platform.Bus.
func (s *Store) purgeThrough(ctx context.Context, subject string, through uint64) error {
	if through == 0 {
		return nil
	}
	if err := raisePurgeFloor(ctx, s.meta, subject, through); err != nil {
		return err
	}
	wire := wireSubject(subject)
	return mapConnErr(s.stream.Purge(ctx, jetstream.WithPurgeSubject(wire), jetstream.WithPurgeSequence(through+1)))
}

func (s *Store) Purge(ctx context.Context, subject string) error {
	if err := checkCallCtx(ctx); err != nil {
		return err
	}
	if err := s.ready(); err != nil {
		return err
	}
	if !ValidSubject(subject) {
		return invalidSubject()
	}
	L, err := lastSeqForSubject(ctx, s.stream, s.meta, subject)
	if err != nil {
		return err
	}
	return s.purgeThrough(ctx, subject, L)
}

// Subscribe implements platform.Bus.
func (s *Store) Subscribe(ctx context.Context, subject string, after uint64) (<-chan platform.Message, error) {
	if err := checkCallCtx(ctx); err != nil {
		return nil, err
	}
	if err := s.ready(); err != nil {
		return nil, err
	}
	if !ValidSubject(subject) {
		return nil, invalidSubject()
	}
	subCtx, cancel := context.WithCancel(ctx)
	var live *liveSubscription
	replayThrough, err := lastSeqForSubject(ctx, s.stream, s.meta, subject)
	if err != nil {
		cancel()
		return nil, err
	}
	live = newLiveSubscription(subject, s.subBuf, after, replayThrough, cancel, func() { s.unregisterLive(live) })
	if err := s.tryRegisterLive(live); err != nil {
		cancel()
		return nil, err
	}
	floor, err := purgeFloor(ctx, s.meta, subject)
	if err != nil {
		live.end()
		return nil, err
	}
	if err := subscribePurged(after, floor); err != nil {
		live.end()
		return nil, err
	}
	start := after + 1
	cons, err := s.js.OrderedConsumer(ctx, streamName, jetstream.OrderedConsumerConfig{
		FilterSubjects:    []string{wireSubject(subject)},
		DeliverPolicy:     jetstream.DeliverByStartSequencePolicy,
		OptStartSeq:       start,
		InactiveThreshold: 3 * time.Second,
	})
	if err != nil {
		live.end()
		return nil, mapConnErr(err)
	}
	if err := floorAfterConsumer(after, func() (uint64, error) {
		return purgeFloor(ctx, s.meta, subject)
	}); err != nil {
		live.end()
		return nil, err
	}
	replayAfter := after
	var cc jetstream.ConsumeContext
	cc, err = cons.Consume(func(msg jetstream.Msg) {
		meta, mdErr := msg.Metadata()
		if mdErr != nil {
			cancel()
			return
		}
		seq := meta.Sequence.Stream
		if replayAfter > 0 && seq <= replayAfter {
			return
		}
		m := platform.Message{Subject: logicalSubject(msg.Subject()), Seq: seq, Data: append([]byte(nil), msg.Data()...)}
		live.deliver(subCtx, m)
	})
	if err != nil {
		live.end()
		return nil, mapConnErr(err)
	}
	live.attach(cc)
	go func() {
		<-subCtx.Done()
		live.end()
	}()
	return live.messages(), nil
}

// Get implements platform.KV.
func (s *Store) Get(ctx context.Context, key string) ([]byte, uint64, error) {
	if err := checkCallCtx(ctx); err != nil {
		return nil, 0, err
	}
	if err := s.ready(); err != nil {
		return nil, 0, err
	}
	if !ValidKey(key) {
		return nil, 0, invalidKey()
	}
	e, err := s.data.Get(ctx, key)
	if err != nil {
		if errorsIsNotFound(err) {
			return nil, 0, notFoundKV()
		}
		return nil, 0, mapConnErr(err)
	}
	rec, err := decodeKV(e.Value())
	if err != nil {
		return nil, 0, kvRecordCorrupt()
	}
	if expired(s.clock, rec) {
		return nil, 0, notFoundKV()
	}
	return rec.Value, e.Revision(), nil
}

// Put implements platform.KV.
func (s *Store) Put(ctx context.Context, key string, value []byte, ttl time.Duration) (uint64, error) {
	if err := checkCallCtx(ctx); err != nil {
		return 0, err
	}
	if err := s.ready(); err != nil {
		return 0, err
	}
	if !ValidKey(key) {
		return 0, invalidKey()
	}
	if err := checkKVValueLen(value, s.publishLimit); err != nil {
		return 0, err
	}
	if ttl < 0 {
		return 0, invalidTTL()
	}
	rec := kvRecord{Value: append([]byte(nil), value...), Deadline: deadlineFromTTL(s.clock.Now(), ttl)}
	rev, err := s.data.Put(ctx, key, encodeKV(rec))
	if err != nil {
		return 0, mapConnErr(err)
	}
	return rev, nil
}

// CompareAndSwap implements platform.KV.
func (s *Store) CompareAndSwap(ctx context.Context, key string, value []byte, revision uint64, ttl time.Duration) (uint64, error) {
	if err := checkCallCtx(ctx); err != nil {
		return 0, err
	}
	if err := s.ready(); err != nil {
		return 0, err
	}
	if !ValidKey(key) {
		return 0, invalidKey()
	}
	if err := checkKVValueLen(value, s.publishLimit); err != nil {
		return 0, err
	}
	if ttl < 0 {
		return 0, invalidTTL()
	}
	rec := kvRecord{Value: append([]byte(nil), value...), Deadline: deadlineFromTTL(s.clock.Now(), ttl)}
	enc := encodeKV(rec)
	if revision == 0 {
		return s.kvCreate(ctx, key, enc)
	}
	cur, err := s.data.Get(ctx, key)
	if err != nil {
		if errorsIsNotFound(err) {
			return 0, conflict("key absent")
		}
		return 0, mapConnErr(err)
	}
	deco, derr := decodeKV(cur.Value())
	if derr != nil {
		return 0, kvRecordCorrupt()
	}
	if expired(s.clock, deco) {
		if cur.Revision() == revision {
			return 0, conflict("entry expired")
		}
		return 0, conflict("key absent")
	}
	if cur.Revision() != revision {
		return 0, conflict("stale revision")
	}
	rev, err := s.data.Update(ctx, key, enc, revision)
	if err != nil {
		if errorsIsConflict(err) {
			return 0, conflict("stale revision")
		}
		return 0, mapConnErr(err)
	}
	return rev, nil
}

func (s *Store) kvCreate(ctx context.Context, key string, enc []byte) (uint64, error) {
	cur, err := s.data.Get(ctx, key)
	if err != nil {
		if errorsIsNotFound(err) {
			rev, err := s.data.Create(ctx, key, enc)
			if err != nil {
				if errorsIsConflict(err) {
					return 0, conflict("key exists")
				}
				return 0, mapConnErr(err)
			}
			return rev, nil
		}
		return 0, mapConnErr(err)
	}
	deco, derr := decodeKV(cur.Value())
	if derr != nil {
		return 0, kvRecordCorrupt()
	}
	if !expired(s.clock, deco) {
		return 0, conflict("key exists")
	}
	rev, err := s.data.Update(ctx, key, enc, cur.Revision())
	if err != nil {
		if errorsIsConflict(err) {
			return 0, conflict("key exists")
		}
		return 0, mapConnErr(err)
	}
	return rev, nil
}

// WriteForeignKVRecord stores raw bytes without encoding (BDD driver for REQ-BUS-026).
func (s *Store) WriteForeignKVRecord(ctx context.Context, key string, raw []byte) error {
	if err := checkCallCtx(ctx); err != nil {
		return err
	}
	if err := s.ready(); err != nil {
		return err
	}
	if !ValidKey(key) {
		return invalidKey()
	}
	_, err := s.data.Put(ctx, key, raw)
	return mapConnErr(err)
}

// Delete implements platform.KV.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := checkCallCtx(ctx); err != nil {
		return err
	}
	if err := s.ready(); err != nil {
		return err
	}
	if !ValidKey(key) {
		return invalidKey()
	}
	err := s.data.Delete(ctx, key)
	if err != nil && !errorsIsNotFound(err) {
		return mapConnErr(err)
	}
	return nil
}
