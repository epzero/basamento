package local

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/runtime"
)

func TestCancelStopsInFlightStepAndWaits(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	entered := make(chan struct{})
	var delivered atomic.Bool
	if err := f.rt.RegisterStep("render", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		close(entered)
		<-ctx.Done()
		delivered.Store(true)
		return nil, ctx.Err()
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("render", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		return nil, dc.Step("render", nil, runtime.StepOptions{}).Get(&out)
	})
	f.start(t, "acme", "c1", "render", `{}`)
	waitOn(t, "the step to start", entered)

	if err := f.rt.Cancel(tenantCtx("acme"), runtime.RunRef{RunID: "c1"}, "user"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !delivered.Load() {
		t.Fatal("Cancel returned before the step observed cancellation")
	}
	if got := f.state(t, "acme", "c1"); got != stateCancelled {
		t.Fatalf("state = %s, want cancelled", got)
	}
}

func TestCancelEndsPendingWaitsWithCancelled(t *testing.T) {
	for _, kind := range []string{"gate", "timer"} {
		t.Run(kind, func(t *testing.T) {
			f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
			defer f.close(t)
			waiting := make(chan struct{})
			failed := make(chan error, 1)
			f.flow("wait", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
				var wait runtime.Future
				if kind == "gate" {
					wait = dc.Await(runtime.GateSpec{Name: "approve"})
				} else {
					wait = dc.Sleep(time.Hour)
				}
				close(waiting)
				err := wait.Get(nil)
				failed <- err
				return nil, err
			})
			f.start(t, "acme", "r1", "wait", `{}`)
			waitOn(t, "the wait to register", waiting)
			if err := f.rt.Cancel(tenantCtx("acme"), runtime.RunRef{RunID: "r1"}, "user"); err != nil {
				t.Fatal(err)
			}
			f.waitState(t, "acme", "r1", stateCancelled)
			err := recvWithin(t, failed, "cancelled wait error")
			if codeOf(err) != "cancelled" {
				t.Fatalf("wait error = %v, want cancelled", err)
			}
		})
	}
}
