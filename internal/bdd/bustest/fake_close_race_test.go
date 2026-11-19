package bustest

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/bus"
)

func TestFakeTeardownWhileReplayBlocked(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 1, MaxMessageSize: bus.DefaultMaxMessageSize})
	subCtx := context.Background()
	s := &sub{
		ch:   make(chan platform.Message, 1),
		done: make(chan struct{}),
	}
	msg1 := platform.Message{Subject: "run.r1.c", Seq: 1, Data: []byte("a")}
	msg2 := platform.Message{Subject: "run.r1.c", Seq: 2, Data: []byte("b")}
	f.replaySend(subCtx, s, msg1)
	if len(s.ch) != 1 {
		t.Fatalf("buffer len %d", len(s.ch))
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		f.replaySend(subCtx, s, msg2)
	}()
	for i := 0; i < 200_000; i++ {
		runtime.Gosched()
	}
	f.terminateSub(s)
	wg.Wait()
	got, ok := <-s.ch
	if !ok || string(got.Data) != "a" {
		t.Fatalf("buffered: ok=%v data=%q", ok, got.Data)
	}
	if _, ok := <-s.ch; ok {
		t.Fatal("channel should be closed")
	}
}
