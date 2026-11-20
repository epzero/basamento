package bus

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/nats-io/nats.go/jetstream"
)

type metaGetSpy struct {
	jetstream.KeyValue
	inner   jetstream.KeyValue
	st      *Store
	subject string
	gets    int
}

func (m *metaGetSpy) Get(ctx context.Context, key string) (jetstream.KeyValueEntry, error) {
	floorKey := metaKey(metaPF, m.subject)
	if key == floorKey {
		m.gets++
		if m.gets == 2 {
			L, err := lastSeqForSubject(ctx, m.st.stream, m.inner, m.subject)
			if err != nil {
				return nil, err
			}
			if err := raisePurgeFloor(ctx, m.inner, m.subject, L); err != nil {
				return nil, err
			}
		}
	}
	return m.KeyValue.Get(ctx, key)
}

func TestSubscribeRejectsRaisedFloorAfterConsumer(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	ctx := context.Background()
	subject := "run.r1.sfloor"
	for i := 0; i < 4; i++ {
		if _, err := st.Publish(ctx, subject, []byte("m")); err != nil {
			t.Fatal(err)
		}
	}
	realMeta := st.meta
	spy := &metaGetSpy{KeyValue: realMeta, inner: realMeta, st: st, subject: subject}
	st.meta = spy
	_, err := st.Subscribe(ctx, subject, 1)
	if err == nil {
		t.Fatal("expected purged")
	}
	if string(core.CodeOf(err)) != string(core.CodeNotFound) {
		t.Fatalf("got %v", err)
	}
	if spy.gets != 2 {
		t.Fatalf("floor Get calls %d want 2", spy.gets)
	}
}
