package steps

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/runtime/local"
)

// TestCancelRunAcceptsAConfirmationRacingTheRunEnd covers MODEL-009-01: when
// the runtime ends a cancelled run, doneCh can be ready while the provider's
// buffered confirmation is still waiting in cancelCh. The select must not
// report the cancel as lost just because it picked doneCh.
func TestCancelRunAcceptsAConfirmationRacingTheRunEnd(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC))
	rt, err := local.New(local.Options{Clock: clock, IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	for i := 0; i < 200; i++ {
		w := newNodeWorld()
		w.rt = rt
		w.ref = runtime.RunRef{}
		w.provider = newNodeProvider(w, "sim-video", model.CapVideoGenerate)
		w.provider.cancelCh <- struct{}{}
		close(w.doneCh)
		if err := w.cancelRun(); err != nil {
			t.Fatalf("iteration %d: cancel confirmed but reported lost: %v", i, err)
		}
	}
}

// TestCancelRunStillFailsWithoutAConfirmation keeps the failure the scenario
// expects when the run ended and the provider never received the cancel.
func TestCancelRunStillFailsWithoutAConfirmation(t *testing.T) {
	clock := coretest.NewFakeClock(time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC))
	rt, err := local.New(local.Options{Clock: clock, IDs: &coretest.SeqIds{}})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	w := newNodeWorld()
	w.rt = rt
	w.ref = runtime.RunRef{}
	w.provider = newNodeProvider(w, "sim-video", model.CapVideoGenerate)
	close(w.doneCh)
	if err := w.cancelRun(); err == nil {
		t.Fatal("cancelRun accepted a run that ended with no cancel request")
	}
}

// TestEventsSignalsTheRunIsFollowed covers the setup MODEL-009-01 needs before
// it cancels: the provider must have told the scenario that the run reached
// the follow step, so the cancel cannot end the run before it is delivered.
func TestEventsSignalsTheRunIsFollowed(t *testing.T) {
	w := newNodeWorld()
	p := newNodeProvider(w, "sim-video", model.CapVideoGenerate)
	p.runs["ref"] = &providerRun{done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := p.Events(ctx, model.Handle{Provider: "sim", ID: "ref", ClientRef: "ref"}, 0); err != nil {
		t.Fatalf("events: %v", err)
	}
	select {
	case <-p.followedCh:
	default:
		t.Fatal("the provider did not signal that the run follows the invocation")
	}
}
