package bus

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/epzero/basamento/core"
)

func TestCallsUnavailableWhenDisconnected(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	st.ForceDisconnect()
	ctx := context.Background()
	subject := "run.r1.down"
	key := "lock.r1"
	check := func(err error, op string) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s succeeded", op)
		}
		if string(core.CodeOf(err)) != string(core.CodeUnavailable) {
			t.Fatalf("%s code %s", op, core.CodeOf(err))
		}
	}
	_, err := st.Publish(ctx, subject, []byte("x"))
	check(err, "Publish")
	_, err = st.LastSeq(ctx, subject)
	check(err, "LastSeq")
	err = st.Purge(ctx, subject)
	check(err, "Purge")
	_, err = st.Subscribe(ctx, subject, 0)
	check(err, "Subscribe")
	_, _, err = st.Get(ctx, key)
	check(err, "Get")
	_, err = st.Put(ctx, key, []byte("v"), 0)
	check(err, "Put")
	_, _, err = st.Get(ctx, key)
	if err == nil {
		t.Fatal("Put wrote while disconnected")
	}
	_, err = st.CompareAndSwap(ctx, key, []byte("v"), 0, 0)
	check(err, "CompareAndSwap")
	err = st.Delete(ctx, key)
	check(err, "Delete")
	if err := st.reconnectInProcess(ctx); err != nil {
		t.Fatal(err)
	}
	_, _, err = st.Get(ctx, key)
	if err == nil || string(core.CodeOf(err)) != string(core.CodeNotFound) {
		t.Fatalf("Get after refused Put: %v", err)
	}
}

func (s *Store) reconnectInProcess(ctx context.Context) error {
	if s.inprocess == nil {
		return unavailable()
	}
	nc, err := nats.Connect("", nats.InProcessServer(s.inprocess), nats.Timeout(2*time.Second))
	if err != nil {
		return mapConnErr(err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return mapConnErr(err)
	}
	stream, err := js.Stream(ctx, streamName)
	if err != nil {
		nc.Close()
		return mapConnErr(err)
	}
	meta, err := js.KeyValue(ctx, metaBucket)
	if err != nil {
		nc.Close()
		return mapConnErr(err)
	}
	data, err := js.KeyValue(ctx, dataBucket)
	if err != nil {
		nc.Close()
		return mapConnErr(err)
	}
	if s.nc != nil {
		s.nc.Close()
	}
	s.nc = nc
	s.js = js
	s.stream = stream
	s.meta = meta
	s.data = data
	return nil
}
