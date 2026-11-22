package steps

import (
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
