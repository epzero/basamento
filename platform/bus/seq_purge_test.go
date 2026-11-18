package bus

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

func openTestEmbedded(t *testing.T) (*Store, string, *coretest.FakeClock) {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "bus-seq-*")
	if err != nil {
		t.Fatal(err)
	}
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	st, err := OpenEmbedded(context.Background(), dir, Options{Clock: clk, SubBuffer: 32})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, dir, clk
}

func TestConcurrentPublishLastSeq(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	const subject = "run.r1.deltas"
	const n = 32
	var wg sync.WaitGroup
	seqs := make([]uint64, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			seq, err := st.Publish(context.Background(), subject, []byte{byte(i)})
			if err != nil {
				t.Error(err)
				return
			}
			seqs[i] = seq
		}(i)
	}
	wg.Wait()
	var max uint64
	for _, s := range seqs {
		if s > max {
			max = s
		}
	}
	last, err := st.LastSeq(context.Background(), subject)
	if err != nil {
		t.Fatal(err)
	}
	if last != max {
		t.Fatalf("LastSeq=%d max publish=%d", last, max)
	}
}

func TestPurgeLastSeqAndSubscribeFloor(t *testing.T) {
	st, dir, _ := openTestEmbedded(t)
	ctx := context.Background()
	subject := "run.r1.msg.a"
	for i := 0; i < 5; i++ {
		if _, err := st.Publish(ctx, subject, []byte{byte('a' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	last, err := st.LastSeq(ctx, subject)
	if err != nil || last != 5 {
		t.Fatalf("last=%d err=%v", last, err)
	}
	if err := st.Purge(ctx, subject); err != nil {
		t.Fatal(err)
	}
	afterPurge, err := st.LastSeq(ctx, subject)
	if err != nil || afterPurge != 5 {
		t.Fatalf("after purge last=%d want 5", afterPurge)
	}
	if _, err := st.Subscribe(ctx, subject, 4); err == nil {
		t.Fatal("subscribe below floor")
	} else if core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("got %s", core.CodeOf(err))
	}
	_ = st.Close()
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	st2, err := OpenEmbedded(ctx, dir, Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st2.Close() }()
	last2, err := st2.LastSeq(ctx, subject)
	if err != nil || last2 != 5 {
		t.Fatalf("restart last=%d", last2)
	}
}

func TestPurgeSequenceBoundKeepsLaterPublish(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	ctx := context.Background()
	subject := "run.r1.events"
	if _, err := st.Publish(ctx, subject, []byte("m1")); err != nil {
		t.Fatal(err)
	}
	L, _ := st.LastSeq(ctx, subject)
	late, err := st.Publish(ctx, subject, []byte("m2"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Purge(ctx, subject); err != nil {
		t.Fatal(err)
	}
	if late <= L {
		t.Fatalf("late seq %d <= L %d", late, L)
	}
	last, err := st.LastSeq(ctx, subject)
	if err != nil {
		t.Fatal(err)
	}
	if last < late {
		t.Fatalf("over-purge: last=%d late=%d", last, late)
	}
	if last != late {
		t.Fatalf("last=%d want late=%d", last, late)
	}
}

func TestEmbeddedDirMode0700(t *testing.T) {
	dir := t.TempDir()
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	st, err := OpenEmbedded(context.Background(), dir, Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	info, err := os.Stat(filepath.Join(dir, "nats"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("perm=%o", info.Mode().Perm())
	}
	dir2 := t.TempDir()
	natsDir := filepath.Join(dir2, "nats")
	if err := os.MkdirAll(natsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	st2, err := OpenEmbedded(context.Background(), dir2, Options{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	_ = st2.Close()
	info2, err := os.Stat(natsDir)
	if err != nil {
		t.Fatal(err)
	}
	if info2.Mode().Perm() != 0o700 {
		t.Fatalf("existing dir perm=%o", info2.Mode().Perm())
	}
}
