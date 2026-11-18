package blob

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestKeyedMuMapReclaimsEntries(t *testing.T) {
	t.Parallel()
	var k keyedMu
	const n = 256
	for i := 0; i < n; i++ {
		key := string(rune('a' + (i % 26)))
		unlock := k.lock(key + string(rune('0'+i/26)))
		unlock()
	}
	if c := k.entryCount(); c != 0 {
		t.Fatalf("map has %d entries, want 0", c)
	}
}

func TestKeyedMuSameKeySerializes(t *testing.T) {
	t.Parallel()
	var k keyedMu
	var holders int32
	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			unlock := k.lock("same")
			if v := atomic.AddInt32(&holders, 1); v != 1 {
				panic("concurrent holders")
			}
			atomic.AddInt32(&holders, -1)
			unlock()
		}()
	}
	wg.Wait()
	if c := k.entryCount(); c != 0 {
		t.Fatalf("map has %d entries after wait, want 0", c)
	}
}

func TestKeyedMuDifferentKeysParallel(t *testing.T) {
	t.Parallel()
	var k keyedMu
	holdA := make(chan struct{})
	releaseA := make(chan struct{})
	go func() {
		unlock := k.lock("a")
		close(holdA)
		<-releaseA
		unlock()
	}()
	<-holdA
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		unlock := k.lock("b")
		unlock()
	}()
	ch := make(chan struct{})
	go func() { wg.Wait(); close(ch) }()
	select {
	case <-ch:
	case <-releaseA:
		t.Fatal("lock on another key blocked while first key is held")
	}
	close(releaseA)
}
