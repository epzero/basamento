package steps

import (
	"testing"
	"time"

	"github.com/epzero/basamento/core/coretest"
)

func TestVRuntimePrunesStoppedTimers(t *testing.T) {
	r := newVRuntime(coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)))
	stopped := r.AfterFunc(time.Second, func() { t.Fatal("stopped timer fired") })
	stopped.Stop()
	live := r.AfterFunc(2*time.Second, func() {})
	defer live.Stop()

	r.mu.Lock()
	_, ok := r.nextDueLocked()
	got := len(r.timers)
	r.mu.Unlock()

	if !ok {
		t.Fatal("next due timer was dropped")
	}
	if got != 1 {
		t.Fatalf("timers = %d, want the stopped one pruned", got)
	}
}
