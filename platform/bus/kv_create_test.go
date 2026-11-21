package bus

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
)

func TestConcurrentCreateExpiredKey(t *testing.T) {
	st, _, clk := openTestEmbedded(t)
	ctx := context.Background()
	key := "lock.r1"
	rev, err := st.Put(ctx, key, []byte("old"), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Second)
	const n = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, conflicts := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := st.CompareAndSwap(ctx, key, []byte{byte('a' + i)}, 0, 0)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				wins++
			} else if string(core.CodeOf(err)) == "conflict" {
				conflicts++
			}
		}(i)
	}
	wg.Wait()
	_ = rev
	if wins != 1 || conflicts != n-1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
}

func TestKVBucketsHaveNoTTL(t *testing.T) {
	st, _, _ := openTestEmbedded(t)
	ctx := context.Background()
	status, err := st.data.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.TTL() > 0 {
		t.Fatalf("data bucket ttl=%v", status.TTL())
	}
	meta, err := st.meta.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if meta.TTL() > 0 {
		t.Fatalf("meta bucket ttl=%v", meta.TTL())
	}
}
