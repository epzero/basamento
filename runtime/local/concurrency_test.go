package local

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/runtime"
)

func TestConcurrentStepsOverlap(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var running, peak atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	for _, name := range []string{"a", "b", "c"} {
		if err := f.rt.RegisterStep(name, func(context.Context, json.RawMessage) (json.RawMessage, error) {
			now := running.Add(1)
			for {
				old := peak.Load()
				if now <= old || peak.CompareAndSwap(old, now) {
					break
				}
			}
			if now == 3 {
				once.Do(func() { close(release) })
			}
			<-release
			running.Add(-1)
			return json.RawMessage(`{}`), nil
		}, runtime.StepDefaults{}); err != nil {
			t.Fatal(err)
		}
	}
	f.flow("fanout", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		first := dc.Step("a", nil, runtime.StepOptions{})
		second := dc.Step("b", nil, runtime.StepOptions{})
		third := dc.Step("c", nil, runtime.StepOptions{})
		if _, err := dc.Select(first, second, third); err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "fanout", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	if got := peak.Load(); got != 3 {
		t.Fatalf("peak concurrent steps = %d, want 3", got)
	}
}

