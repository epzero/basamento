package bus

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
)

func openTestEmbeddedBuf(t *testing.T, buf int) (*Store, string, *coretest.FakeClock) {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "bus-replay-*")
	if err != nil {
		t.Fatal(err)
	}
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	st, err := OpenEmbedded(context.Background(), dir, Options{Clock: clk, SubBuffer: buf})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, dir, clk
}

func TestReplay2000MessagesInOrder(t *testing.T) {
	st, _, _ := openTestEmbeddedBuf(t, 2048)
	ctx := context.Background()
	const subject = "run.r1.events"
	const n = 2000
	for i := 1; i <= n; i++ {
		if _, err := st.Publish(ctx, subject, []byte(fmt.Sprintf("m%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := st.Subscribe(subCtx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	var last uint64
	for i := 0; i < n; i++ {
		m, ok := <-ch
		if !ok {
			t.Fatalf("ended at %d", i)
		}
		if m.Seq <= last {
			t.Fatalf("seq %d after %d", m.Seq, last)
		}
		last = m.Seq
	}
}

func TestSubscribeAfterSeqSkipsEarlierMessages(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	ctx := context.Background()
	const subject = "run.r1.deltas"
	for i := 1; i <= 2; i++ {
		if _, err := st.Publish(ctx, subject, []byte(fmt.Sprintf("m%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	subCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := st.Subscribe(subCtx, subject, 2)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
		t.Fatal("unexpected message before m3")
	default:
	}
	if _, err := st.Publish(ctx, subject, []byte("m3")); err != nil {
		t.Fatal(err)
	}
	m, ok := <-ch
	if !ok || string(m.Data) != "m3" {
		t.Fatalf("got %v ok=%v", m, ok)
	}
}
