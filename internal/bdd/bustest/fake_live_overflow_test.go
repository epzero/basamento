package bustest

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform/bus"
)

func TestFakeLiveOverflowDuringReplayMatchesStore(t *testing.T) {
	t.Run("scheduleA_replayBeforeLive", TestFakeLiveOverflowScheduleA)
	t.Run("scheduleB_liveDuringReplay", TestFakeLiveOverflowScheduleB)
}

// Schedule A: replay fills the buffer, then live publish sets dropLive.
func TestFakeLiveOverflowScheduleA(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
	subject := "run.r1.of"
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if _, err := f.Publish(ctx, subject, []byte{byte('a' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := f.Subscribe(ctx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	ended := f.SubscriptionDone(subject)
	if ended == nil {
		t.Fatal("no subscription done channel")
	}
	settled := f.ReplaySettled(subject)
	if settled == nil {
		t.Fatal("no replay settled channel")
	}
	<-settled
	if _, err := f.Publish(ctx, subject, []byte("live")); err != nil {
		t.Fatal(err)
	}
	if !f.SubscriptionEnded(subject) {
		t.Fatal("subscription should end immediately after live overflow")
	}
	<-ended
	for i := 0; i < 4; i++ {
		if _, ok := <-ch; !ok {
			t.Fatalf("ended at %d", i)
		}
	}
	if _, ok := <-ch; ok {
		t.Fatal("subscription should have ended on overflow")
	}
}

// Schedule B: live publish while replay is still gated.
func TestFakeLiveOverflowScheduleB(t *testing.T) {
	clk := coretest.NewFakeClock(time.Unix(0, 0))
	f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
	subject := "run.r1.of"
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if _, err := f.Publish(ctx, subject, []byte{byte('a' + i)}); err != nil {
			t.Fatal(err)
		}
	}
	f.ArmReplayGate()
	ch, err := f.Subscribe(ctx, subject, 0)
	if err != nil {
		t.Fatal(err)
	}
	ended := f.SubscriptionDone(subject)
	if ended == nil {
		t.Fatal("no subscription done channel")
	}
	liveDone := make(chan struct{})
	go func() {
		_, _ = f.Publish(ctx, subject, []byte("live"))
		close(liveDone)
	}()
	<-liveDone
	f.ReleaseReplayGate()
	<-ended
	for {
		if _, ok := <-ch; !ok {
			break
		}
	}
}
