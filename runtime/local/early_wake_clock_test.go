package local

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/runtime"
)

// manualAfterClock is a FakeClock whose After channels are fired by the test
// without advancing Now(). Every After call is sent on afterCalls.
type manualAfterClock struct {
	*coretest.FakeClock
	afterCalls chan time.Duration

	mu      sync.Mutex
	pending []*manualWake
}

type manualWake struct {
	d    time.Duration
	fire chan struct{}
	out  chan time.Time
}

func newManualAfterClock(at time.Time) *manualAfterClock {
	return &manualAfterClock{
		FakeClock:  coretest.NewFakeClock(at),
		afterCalls: make(chan time.Duration, 8),
	}
}

func (c *manualAfterClock) After(d time.Duration) <-chan time.Time {
	w := &manualWake{d: d, fire: make(chan struct{}), out: make(chan time.Time, 1)}
	c.mu.Lock()
	c.pending = append(c.pending, w)
	c.mu.Unlock()
	c.afterCalls <- d
	go func() {
		<-w.fire
		w.out <- time.Time{}
	}()
	return w.out
}

func (c *manualAfterClock) fire(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, w := range c.pending {
		if w.d == d {
			close(w.fire)
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			return
		}
	}
}

func TestSleepRearmsOnEarlyWake(t *testing.T) {
	clock := newManualAfterClock(time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	f := &fixture{clock: clock.FakeClock, host: testHost{}}
	var err error
	f.rt, err = New(Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.rt.RegisterFlowHost(f.host.Run); err != nil {
		t.Fatal(err)
	}
	defer f.close(t)

	sleeping := make(chan struct{})
	done := make(chan error, 1)
	f.flow("timer", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Sleep(10 * time.Second)
		close(sleeping)
		done <- wait.Get(nil)
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "timer", `{}`)
	waitOn(t, "sleep to register", sleeping)

	first := recvWithin(t, clock.afterCalls, "first After")
	if first != 10*time.Second {
		t.Fatalf("first After %v, want 10s", first)
	}
	clock.Set(clock.Now().Add(4 * time.Second))
	clock.fire(10 * time.Second)

	second := recvWithin(t, clock.afterCalls, "re-arm After")
	if second != 6*time.Second {
		t.Fatalf("re-arm After %v, want 6s", second)
	}
	pollUntil(t, "sleep still running", func() bool { return f.state(t, "acme", "r1") == stateRunning })
	select {
	case err := <-done:
		t.Fatalf("sleep completed early: %v", err)
	default:
	}
	clock.Set(clock.Now().Add(6 * time.Second))
	clock.fire(6 * time.Second)
	if err := <-done; err != nil {
		t.Fatalf("sleep after real deadline: %v", err)
	}
	f.waitState(t, "acme", "r1", stateCompleted)
}

func TestGateTimeoutRearmsOnEarlyWake(t *testing.T) {
	clock := newManualAfterClock(time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	f := &fixture{clock: clock.FakeClock, host: testHost{}}
	var err error
	f.rt, err = New(Options{Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.rt.RegisterFlowHost(f.host.Run); err != nil {
		t.Fatal(err)
	}
	defer f.close(t)

	atGate := make(chan struct{})
	f.flow("waiting", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: "approve", Timeout: time.Hour})
		close(atGate)
		return nil, wait.Get(nil)
	})
	f.start(t, "acme", "r1", "waiting", `{}`)
	waitOn(t, "gate wait", atGate)

	first := recvWithin(t, clock.afterCalls, "gate timeout After")
	if first != time.Hour {
		t.Fatalf("first After %v, want 1h", first)
	}
	clock.Set(clock.Now().Add(10 * time.Minute))
	clock.fire(time.Hour)

	second := recvWithin(t, clock.afterCalls, "gate timeout re-arm")
	if second != 50*time.Minute {
		t.Fatalf("re-arm %v, want 50m", second)
	}
	pollUntil(t, "still running after early wake", func() bool { return f.state(t, "acme", "r1") == stateRunning })
	if err := f.rt.Resolve(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}, "approve", json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	f.waitState(t, "acme", "r1", stateCompleted)
}