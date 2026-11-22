package steps

import (
	"testing"
	"time"

	"github.com/epzero/basamento/model"
)

// TestAwaitCancelAcceptsAConfirmationRacingTheDeadline covers the timeout
// branch of cancelRun: when the provider's buffered confirmation is ready just
// as the wait bound expires, cancelCh and the deadline are ready together and
// the select may pick the deadline although the cancel was delivered. The
// timeout case drains the confirmation before reporting.
func TestAwaitCancelAcceptsAConfirmationRacingTheDeadline(t *testing.T) {
	deadline := make(chan time.Time)
	close(deadline)
	for i := 0; i < 200; i++ {
		w := newNodeWorld()
		w.provider = newNodeProvider(w, "sim-video", model.CapVideoGenerate)
		w.provider.cancelCh <- struct{}{}
		if err := w.awaitCancel(deadline); err != nil {
			t.Fatalf("iteration %d: cancel confirmed but reported lost: %v", i, err)
		}
	}
}

// TestAwaitCancelStillTimesOutWithoutAConfirmation keeps the failure the
// scenario expects when the bound expires and the provider never received the
// cancel.
func TestAwaitCancelStillTimesOutWithoutAConfirmation(t *testing.T) {
	deadline := make(chan time.Time)
	close(deadline)
	w := newNodeWorld()
	w.provider = newNodeProvider(w, "sim-video", model.CapVideoGenerate)
	if err := w.awaitCancel(deadline); err == nil {
		t.Fatal("awaitCancel accepted a wait bound that expired with no cancel request")
	}
}
