package local

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/epzero/basamento/core"
)

// runTimer is one pending clock wait (a Sleep or a gate timeout). Helpers wake
// on wake and call fireDue; they never complete the future themselves.
type runTimer struct {
	deadline time.Time
	future   *future
	wake     <-chan time.Time
	gate     *gate // set for gate timeouts so fireDue can emit CodeTimeout
}

func (r *run) removeTimer(target *runTimer) {
	kept := r.timers[:0]
	for _, timer := range r.timers {
		if timer != target {
			kept = append(kept, timer)
		}
	}
	r.timers = kept
}

func (r *run) timerOutcome(timer *runTimer) (json.RawMessage, error) {
	if timer.gate == nil {
		return nil, nil
	}
	g := timer.gate
	if g.resolved {
		return nil, nil
	}
	return nil, core.NewError(core.CodeTimeout,
		fmt.Sprintf("local: gate %q timed out", g.spec.Name),
		map[string]any{"gate": g.spec.Name})
}

// fireDue completes every pending timer whose deadline the clock has reached,
// all with one completion seq (REQ-RT-008, REQ-RT-011). It is idempotent when
// the signaled timer already completed. An early wake before the deadline
// leaves the signaled timer registered and returns the remaining duration for
// re-arm.
func (r *run) fireDue(signaled *runTimer) time.Duration {
	r.mu.Lock()
	if signaled.future.Ready() {
		r.mu.Unlock()
		return 0
	}
	now := r.rt.clock.Now()
	seq := r.seqs.Add(1)
	var completed bool
	complete := func(timer *runTimer) {
		if timer.future.Ready() {
			return
		}
		if timer.deadline.After(now) {
			return
		}
		out, err := r.timerOutcome(timer)
		if timer.future.completeWithSeq(seq, out, err, false) {
			completed = true
		}
	}
	complete(signaled)
	var rearm time.Duration
	if !signaled.future.Ready() && signaled.deadline.After(now) {
		rearm = signaled.deadline.Sub(now)
	}
	kept := r.timers[:0]
	for _, timer := range r.timers {
		if timer == signaled {
			if !signaled.future.Ready() {
				kept = append(kept, signaled)
			}
			continue
		}
		if timer.future.Ready() {
			continue
		}
		if timer.deadline.After(now) {
			kept = append(kept, timer)
			continue
		}
		complete(timer)
	}
	r.timers = kept
	r.mu.Unlock()
	if completed {
		r.notify()
	}
	return rearm
}

// waitTimer waits for timer wakes until the timer future is ready, the run
// context is cancelled (onCancel runs once), or stop is closed when non-nil.
// A nil stop never selects on stop (used by Sleep). An early After wake
// re-arms for the remaining duration because REQ-RT-008 requires the runtime
// clock to reach the deadline before the timer completes.
func (r *run) waitTimer(timer *runTimer, stop <-chan struct{}, onCancel func()) {
	for {
		select {
		case <-timer.wake:
			remaining := r.fireDue(timer)
			if timer.future.Ready() {
				return
			}
			if remaining > 0 {
				timer.wake = r.rt.after(remaining)
			}
		case <-stop:
			return
		case <-r.ctx.Done():
			onCancel()
			return
		}
	}
}
