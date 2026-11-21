package bustest

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform/bus"
)

func TestFakeReplayGateSubscribeReleaseRace(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
	subject := "run.r1.race"
	ctx := context.Background()
	if _, err := f.Publish(ctx, subject, []byte("m")); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		f.ArmReplayGate()
		iterCtx, cancel := context.WithCancel(ctx)
		ch, err := f.Subscribe(iterCtx, subject, 0)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		f.ReleaseReplayGate()
		if _, ok := <-ch; !ok {
			cancel()
			t.Fatal("replay message missing")
		}
		cancel()
		for range ch {
		}
	}
}
