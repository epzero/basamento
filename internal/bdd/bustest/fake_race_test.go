package bustest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform/bus"
)

func TestFakeConcurrentPubSubRace(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{Clock: clk, SubBuffer: 8})
	ctx := context.Background()
	const subject = "run.r1.events"
	for iter := 0; iter < 32; iter++ {
		subCtx, cancel := context.WithCancel(ctx)
		ch, err := f.Subscribe(subCtx, subject, 0)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		for p := 0; p < 4; p++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = f.Publish(ctx, subject, []byte("x"))
			}()
		}
		wg.Wait()
		go func() {
			for {
				select {
				case _, ok := <-ch:
					if !ok {
						return
					}
				case <-subCtx.Done():
					return
				}
			}
		}()
		cancel()
	}
}
