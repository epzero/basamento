package steps

import "testing"

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
	dc.waitSettled()
}

// A completed run must keep unblocking the same wait path: markFinished closes
// finished, so waitSettled returns even when settle is nil and no gate was hit.
func TestConvDCWaitSettledAfterFinishReturns(t *testing.T) {
	dc := newConvDC(newConvState("acme", "c1", "ana", "assistant"))
	defer dc.stop()

	dc.markFinished()
	dc.waitSettled()
}
