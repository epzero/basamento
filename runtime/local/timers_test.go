package local

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/epzero/basamento/runtime"
)

func TestTimerOnlyCompletesWhenClockReachesIt(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	sleeping := make(chan struct{})
	f.flow("timer", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Sleep(30 * time.Minute)
		close(sleeping)
		if err := wait.Get(nil); err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), nil
	})
	ref := f.start(t, "acme", "r1", "timer", `{}`)
	if ref.RunID != "r1" || ref.TenantID != "acme" {
		t.Fatalf("ref %+v, want r1 in acme", ref)
	}
	waitOn(t, "the timer to register", sleeping)

	f.clock.Advance(29 * time.Minute)
	if got := f.state(t, "acme", "r1"); got != stateRunning {
		t.Fatalf("state after 29m = %s, want running", got)
	}
	f.clock.Advance(time.Minute)
	f.waitState(t, "acme", "r1", stateCompleted)
}

