package bustest

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/bus"
)

func TestFakeLiveOverflowDuringReplayMatchesStore(t *testing.T) {
	t.Run("scheduleA_replayBeforeLive", TestFakeLiveOverflowScheduleA)
	t.Run("scheduleB_liveDuringReplay", TestFakeLiveOverflowScheduleB)
	for _, tc := range fakeOverflowShapes {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tc.runFake(t)
		})
	}
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
	<-ended
	assertRecvSeqs(t, ch, []uint64{1, 2, 3, 4})
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
	assertRecvSeqs(t, ch, []uint64{1, 2, 3, 4})
}

type fakeOverflowCase struct {
	name    string
	runFake func(t *testing.T)
}

var fakeOverflowShapes = []fakeOverflowCase{
	{
		name: "a_overflowBeforeReplayRequested",
		runFake: func(t *testing.T) {
			clk := coretest.NewFakeClock(time.Unix(0, 0))
			f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
			subject := "run.r1.sh.a"
			ctx := context.Background()
			for i := 0; i < 4; i++ {
				if _, err := f.Publish(ctx, subject, []byte{byte('a' + i)}); err != nil {
					t.Fatal(err)
				}
			}
			ch, err := f.Subscribe(ctx, subject, 4)
			if err != nil {
				t.Fatal(err)
			}
			ended := f.SubscriptionDone(subject)
			for i := 0; i < 4; i++ {
				if _, err := f.Publish(ctx, subject, []byte{byte('l' + i)}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.Publish(ctx, subject, []byte("overflow")); err != nil {
				t.Fatal(err)
			}
			<-ended
			assertRecvSeqs(t, ch, []uint64{5, 6, 7, 8})
		},
	},
	{
		name: "c_gatedReplayOccupiedFull",
		runFake: func(t *testing.T) {
			clk := coretest.NewFakeClock(time.Unix(0, 0))
			f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
			subject := "run.r1.sh.c"
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
			if !f.SubscriptionOccupiedAtLeast(subject, 4) {
				t.Fatal("expected replay to occupy the full buffer before gate release")
			}
			if _, err := f.Publish(ctx, subject, []byte("live")); err != nil {
				t.Fatal(err)
			}
			f.ReleaseReplayGate()
			<-ended
			assertRecvSeqs(t, ch, []uint64{1, 2, 3, 4})
		},
	},
	{
		name:    "b_gatedReplayBufferHasRoom",
		runFake: TestFakeLiveOverflowScheduleB,
	},
	{
		name:    "d_overflowAfterReplayFinished",
		runFake: TestFakeLiveOverflowScheduleA,
	},
	{
		name: "e_twoLiveDuringGate",
		runFake: func(t *testing.T) {
			clk := coretest.NewFakeClock(time.Unix(0, 0))
			f := NewFake(clk, bus.Options{SubBuffer: 4, MaxMessageSize: bus.DefaultMaxMessageSize})
			subject := "run.r1.sh.e"
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
			if _, err := f.Publish(ctx, subject, []byte("live1")); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Publish(ctx, subject, []byte("live2")); err != nil {
				t.Fatal(err)
			}
			f.ReleaseReplayGate()
			<-ended
			assertRecvSeqs(t, ch, []uint64{1, 2, 3, 4})
		},
	},
}

func assertRecvSeqs(t *testing.T, ch <-chan platform.Message, want []uint64) {
	t.Helper()
	for i, w := range want {
		m, ok := <-ch
		if !ok {
			t.Fatalf("ended at %d want seq %d", i, w)
		}
		if m.Seq != w {
			t.Fatalf("[%d] seq=%d want %d", i, m.Seq, w)
		}
	}
	if _, ok := <-ch; ok {
		t.Fatal("subscription should have ended on overflow")
	}
}
