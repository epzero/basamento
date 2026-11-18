package bus

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
)

const (
	streamName   = "KIT_BUS"
	metaBucket   = "KIT_BUS_META"
	dataBucket   = "KIT_KV"
	metaPF       = "pf."
	metaLS       = "ls."
	metaLR       = "lr."
	serverTTLPad = 365 * 24 * time.Hour // backstop cleanup only
)

// Store implements platform.Bus and platform.KV over one JetStream deployment.
type Store struct {
	clock   core.Clock
	nc      *nats.Conn
	js      jetstream.JetStream
	meta    jetstream.KeyValue
	data    jetstream.KeyValue
	subBuf  int
	maxSize int

	closeSrv func()
	stopOnce sync.Once
}

type Options struct {
	Clock          core.Clock
	SubBuffer      int
	MaxMessageSize int
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
	return o
}

func connectAndOpen(nc *nats.Conn, opts Options, closeSrv func()) (*Store, error) {
	opts = opts.withDefaults()
	if err := nc.FlushTimeout(2 * time.Second); err != nil {
		nc.Close()
		return nil, mapConnErr(err)
	}
	if nc.Status() != nats.CONNECTED {
		nc.Close()
		return nil, unavailable()
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, mapConnErr(err)
	}
	ctx := context.Background()
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        streamName,
		Subjects:    []string{busSubjectPrefix + ">"},
		Storage:     jetstream.FileStorage,
		MaxMsgSize:  int32(opts.MaxMessageSize),
		AllowDirect: true,
	}); err != nil {
		nc.Close()
		return nil, mapConnErr(err)
	}
	meta, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: metaBucket,
	})
	if err != nil {
		nc.Close()
		return nil, mapConnErr(err)
	}
	data, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket: dataBucket,
		TTL:    serverTTLPad,
	})
	if err != nil {
		nc.Close()
		return nil, mapConnErr(err)
	}
	if closeSrv == nil {
		closeSrv = func() {}
	}
	return &Store{
		clock:    opts.Clock,
		nc:       nc,
		js:       js,
		meta:     meta,
		data:     data,
		subBuf:   opts.SubBuffer,
		maxSize:  opts.MaxMessageSize,
		closeSrv: closeSrv,
	}, nil
}

// Close releases connections and, for embedded servers, stops the in-process NATS.
func (s *Store) Close() error {
	s.stopOnce.Do(func() {
		if s.nc != nil {
			s.nc.Close()
		}
		if s.closeSrv != nil {
			s.closeSrv()
		}
	})
	return nil
}

func (s *Store) ready() error {
	if s.nc == nil || s.nc.Status() != nats.CONNECTED {
		return unavailable()
	}
	return nil
}

// Connected reports whether the NATS connection is up (tests only).
func (s *Store) Connected() bool {
	return s.nc != nil && s.nc.Status() == nats.CONNECTED
}

func metaKey(prefix, subject string) string { return prefix + subject }

func uint64Bytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func parseUint64(b []byte) uint64 {
	if len(b) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

func (s *Store) metaGet(ctx context.Context, key string) (uint64, bool, error) {
	e, err := s.meta.Get(ctx, key)
	if err != nil {
		if errorsIsNotFound(err) {
			return 0, false, nil
		}
		return 0, false, mapConnErr(err)
	}
	return parseUint64(e.Value()), true, nil
}

func (s *Store) metaPut(ctx context.Context, key string, v uint64) error {
	_, err := s.meta.Put(ctx, key, uint64Bytes(v))
	return mapConnErr(err)
}

func (s *Store) purgeFloor(ctx context.Context, subject string) (uint64, error) {
	v, _, err := s.metaGet(ctx, metaKey(metaPF, subject))
	return v, err
}

func (s *Store) bumpSeqMeta(ctx context.Context, subject string, seq uint64) error {
	lsKey := metaKey(metaLS, subject)
	lrKey := metaKey(metaLR, subject)
	cur, _, err := s.metaGet(ctx, lsKey)
	if err != nil {
		return err
	}
	if seq > cur {
		if err := s.metaPut(ctx, lsKey, seq); err != nil {
			return err
		}
	}
	return s.metaPut(ctx, lrKey, seq)
}

// Publish implements platform.Bus.
func (s *Store) Publish(ctx context.Context, subject string, data []byte) (uint64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if !ValidSubject(subject) {
		return 0, invalidSubject()
	}
	if len(data) > s.maxSize {
		return 0, messageTooLarge()
	}
	ack, err := s.js.Publish(ctx, wireSubject(subject), data)
	if err != nil {
		return 0, mapConnErr(err)
	}
	seq := ack.Sequence
	if err := s.bumpSeqMeta(ctx, subject, seq); err != nil {
		return 0, err
	}
	return seq, nil
}

// LastSeq implements platform.Bus.
func (s *Store) LastSeq(ctx context.Context, subject string) (uint64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if !ValidSubject(subject) {
		return 0, invalidSubject()
	}
	v, _, err := s.metaGet(ctx, metaKey(metaLS, subject))
	return v, err
}

// Purge implements platform.Bus.
func (s *Store) Purge(ctx context.Context, subject string) error {
	if err := s.ready(); err != nil {
		return err
	}
	if !ValidSubject(subject) {
		return invalidSubject()
	}
	lr, _, err := s.metaGet(ctx, metaKey(metaLR, subject))
	if err != nil {
		return err
	}
	if lr > 0 {
		curPF, _, err := s.metaGet(ctx, metaKey(metaPF, subject))
		if err != nil {
			return err
		}
		if lr > curPF {
			if err := s.metaPut(ctx, metaKey(metaPF, subject), lr); err != nil {
				return err
			}
		}
	}
	str, err := s.js.Stream(ctx, streamName)
	if err != nil {
		return mapConnErr(err)
	}
	if err := str.Purge(ctx, jetstream.WithPurgeSubject(wireSubject(subject))); err != nil {
		return mapConnErr(err)
	}
	_ = s.meta.Delete(ctx, metaKey(metaLR, subject))
	return nil
}

// Subscribe implements platform.Bus.
func (s *Store) Subscribe(ctx context.Context, subject string, after uint64) (<-chan platform.Message, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if !ValidSubject(subject) {
		return nil, invalidSubject()
	}
	floor, err := s.purgeFloor(ctx, subject)
	if err != nil {
		return nil, err
	}
	if after > 0 && after < floor {
		return nil, purged(floor)
	}
	start := after + 1
	if after == 0 {
		start = 1
	}
	cons, err := s.js.OrderedConsumer(ctx, streamName, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{wireSubject(subject)},
		DeliverPolicy:  jetstream.DeliverByStartSequencePolicy,
		OptStartSeq:    start,
	})
	if err != nil {
		return nil, mapConnErr(err)
	}
	ch := make(chan platform.Message, s.subBuf)
	subCtx, cancel := context.WithCancel(ctx)
	var ended sync.Once
	end := func() {
		ended.Do(func() {
			cancel()
			close(ch)
		})
	}
	var cc jetstream.ConsumeContext
	cc, err = cons.Consume(func(msg jetstream.Msg) {
		meta, _ := msg.Metadata()
		var seq uint64
		if meta != nil {
			seq = meta.Sequence.Stream
		}
		m := platform.Message{Subject: logicalSubject(msg.Subject()), Seq: seq, Data: append([]byte(nil), msg.Data()...)}
		select {
		case <-subCtx.Done():
			return
		default:
		}
		if len(ch) >= s.subBuf {
			end()
			cc.Stop()
			return
		}
		select {
		case <-subCtx.Done():
		case ch <- m:
		}
	}, jetstream.PullMaxMessages(1))
	if err != nil {
		cancel()
		return nil, mapConnErr(err)
	}
	go func() {
		<-subCtx.Done()
		cc.Stop()
		end()
	}()
	return ch, nil
}

// Get implements platform.KV.
func (s *Store) Get(ctx context.Context, key string) ([]byte, uint64, error) {
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
		return nil, 0, err
	}
	if expired(s.clock, rec) {
		return nil, 0, notFoundKV()
	}
	return rec.Value, e.Revision(), nil
}

// Put implements platform.KV.
func (s *Store) Put(ctx context.Context, key string, value []byte, ttl time.Duration) (uint64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if !ValidKey(key) {
		return 0, invalidKey()
	}
	if ttl < 0 {
		return 0, invalidTTL()
	}
	var deadline time.Time
	if ttl > 0 {
		deadline = s.clock.Now().Add(ttl)
	}
	rec := kvRecord{Value: append([]byte(nil), value...), Deadline: deadline}
	rev, err := s.data.Put(ctx, key, encodeKV(rec))
	if err != nil {
		return 0, mapConnErr(err)
	}
	return rev, nil
}

// CompareAndSwap implements platform.KV.
func (s *Store) CompareAndSwap(ctx context.Context, key string, value []byte, revision uint64, ttl time.Duration) (uint64, error) {
	if err := s.ready(); err != nil {
		return 0, err
	}
	if !ValidKey(key) {
		return 0, invalidKey()
	}
	if ttl < 0 {
		return 0, invalidTTL()
	}
	cur, err := s.data.Get(ctx, key)
	if revision == 0 {
		if err == nil {
			rec, derr := decodeKV(cur.Value())
			if derr == nil && !expired(s.clock, rec) {
				return 0, conflict("key exists")
			}
			_ = s.data.Delete(ctx, key)
		} else if !errorsIsNotFound(err) {
			return 0, mapConnErr(err)
		}
		var deadline time.Time
		if ttl > 0 {
			deadline = s.clock.Now().Add(ttl)
		}
		rec := kvRecord{Value: append([]byte(nil), value...), Deadline: deadline}
		rev, err := s.data.Create(ctx, key, encodeKV(rec))
		if err != nil {
			if errorsIsConflict(err) {
				return 0, conflict("key exists")
			}
			return 0, mapConnErr(err)
		}
		return rev, nil
	}
	if err != nil {
		if errorsIsNotFound(err) {
			return 0, conflict("key absent")
		}
		return 0, mapConnErr(err)
	}
	rec, derr := decodeKV(cur.Value())
	if derr != nil {
		return 0, derr
	}
	if expired(s.clock, rec) {
		if revision != 0 && cur.Revision() == revision {
			return 0, conflict("entry expired")
		}
		return 0, conflict("key absent")
	}
	if cur.Revision() != revision {
		return 0, conflict("stale revision")
	}
	var deadline time.Time
	if ttl > 0 {
		deadline = s.clock.Now().Add(ttl)
	}
	newRec := kvRecord{Value: append([]byte(nil), value...), Deadline: deadline}
	rev, err := s.data.Update(ctx, key, encodeKV(newRec), revision)
	if err != nil {
		if errorsIsConflict(err) {
			return 0, conflict("stale revision")
		}
		return 0, mapConnErr(err)
	}
	return rev, nil
}

// Delete implements platform.KV.
func (s *Store) Delete(ctx context.Context, key string) error {
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

// SetSubBuffer overrides the subscription buffer size (for tests).
func (s *Store) SetSubBuffer(n int) {
	if n > 0 {
		s.subBuf = n
	}
}

// SetMaxMessageSize overrides the publish size limit (for tests).
func (s *Store) SetMaxMessageSize(n int) {
	if n > 0 {
		s.maxSize = n
	}
}

func errorsIsNotFound(err error) bool {
	return errors.Is(err, jetstream.ErrKeyNotFound)
}

func errorsIsConflict(err error) bool {
	return errors.Is(err, jetstream.ErrKeyExists) || errors.Is(err, jetstream.ErrKeyDeleted)
}
