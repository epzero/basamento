package steps

import (
	"context"
	"testing"
)

// EP0-232: the nightly flake-hunt caught a lost wakeup. When the coordinator
// reached its first gate before start called waitSettled, blocked cleared
// settle and waitSettled installed a fresh channel that nobody would close.
// The wanted ordering is deterministic, not timing-dependent: block first,
// await second.
func TestConvDCWaitSettledAfterBlockReturns(t *testing.T) {
	dc := newConvDC(newConvState("acme", "c1", "ana", "assistant"))
	defer dc.stop()

	// The coordinator blocks at its gate before the harness awaits the settle.
	dc.blocked()

	// Bound the wait: on a regression (the atGate guard gone) waitSettled would
	// block the whole test binary until the runner's timeout, which is exactly
	// the nightly hang this change exists to prevent.
	done := make(chan struct{})
	go func() {
		dc.waitSettled()
		close(done)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), waitBound)
	defer cancel()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("waitSettled did not return within %v after the coordinator blocked", waitBound)
	}
}

// A completed run must keep unblocking the same wait path: markFinished closes
// finished, so waitSettled returns even when settle is nil and no gate was hit.
func TestConvDCWaitSettledAfterFinishReturns(t *testing.T) {
	dc := newConvDC(newConvState("acme", "c1", "ana", "assistant"))
	defer dc.stop()

	dc.markFinished()
	dc.waitSettled()
}
