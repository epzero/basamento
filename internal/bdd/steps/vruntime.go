package steps

import (
	"context"
	"sync"
	"time"

	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/model/middleware"
)

// vRuntime is the BDD runtime: virtual time from the scenario's fake clock,
// timers that fire when that clock reaches them, and one participant goroutine
// (the call) that parks in Sleep and is woken by Advance. Advance returns only
// when the call is parked again or finished, so a scenario can assert on a
// stable state after "N seconds pass" without a sleep or a wall clock.
type vRuntime struct {
	mu    sync.Mutex
	cond  *sync.Cond
	clock *coretest.FakeClock

	timers []*vTimer

	parked        bool
	parkedGen     int64
	gen           int64
	sleepDeadline time.Time
	finished      bool
	closed        bool
}

type vTimer struct {
	when    time.Time
	f       func()
	stopped bool
}

func newVRuntime(clock *coretest.FakeClock) *vRuntime {
	r := &vRuntime{clock: clock}
	r.cond = sync.NewCond(&r.mu)
	return r
}

// Now returns the scenario clock's time.
func (r *vRuntime) Now() time.Time { return r.clock.Now() }

// Sleep parks the participant until d has elapsed on the scenario clock or ctx
// ends. Advance wakes it; a timer callback that cancels ctx wakes it too.
func (r *vRuntime) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return context.Canceled
	}
	if d <= 0 {
		return nil
	}
	deadline := r.clock.Now().Add(d)
	for {
		if err := ctx.Err(); err != nil {
			r.parked = false
			return err
		}
		if !r.clock.Now().Before(deadline) {
			r.parked = false
			return nil
		}
		r.parked = true
		r.parkedGen = r.gen
		r.sleepDeadline = deadline
		r.cond.Broadcast()
		for r.gen == r.parkedGen && !r.closed {
			r.cond.Wait()
		}
		r.parked = false
		if r.closed {
			return context.Canceled
		}
	}
}

// AfterFunc schedules f when the scenario clock reaches d from now.
func (r *vRuntime) AfterFunc(d time.Duration, f func()) middleware.Timer {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := &vTimer{when: r.clock.Now().Add(d), f: f}
	r.timers = append(r.timers, t)
	return &vTimerHandle{rt: r, timer: t}
}

type vTimerHandle struct {
	rt    *vRuntime
	timer *vTimer
}

func (h *vTimerHandle) Stop() {
	h.rt.mu.Lock()
	defer h.rt.mu.Unlock()
	h.timer.stopped = true
}

// finish marks the participant as done and wakes Advance.
func (r *vRuntime) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished = true
	r.parked = false
	r.gen++
	r.cond.Broadcast()
}

// close releases a call left parked at the end of a scenario.
func (r *vRuntime) close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.gen++
	r.cond.Broadcast()
}

// waitReady blocks until the participant has parked or finished, without
// moving time.
func (r *vRuntime) waitReady() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.waitParkedLocked()
}

// autoPump advances virtual time to the next due timer or sleep until the call
// finishes.
func (r *vRuntime) autoPump() {
	for {
		r.mu.Lock()
		r.waitParkedLocked()
		if r.finished || r.closed {
			r.mu.Unlock()
			return
		}
		next, ok := r.nextDueLocked()
		if !ok {
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		r.advance(next.Sub(r.clock.Now()))
	}
}

// Advance moves virtual time by d, firing every timer and waking the
// participant at each due instant in order.
func (r *vRuntime) Advance(d time.Duration) {
	if d < 0 {
		return
	}
	target := r.clock.Now().Add(d)
	r.advanceTo(target)
}

func (r *vRuntime) advance(d time.Duration) {
	r.advanceTo(r.clock.Now().Add(d))
}

func (r *vRuntime) advanceTo(target time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		r.waitParkedLocked()
		if r.finished || r.closed {
			r.moveClockLocked(target)
			return
		}
		timer := r.nextTimerLocked(target)
		sleepDue := r.parked && !r.sleepDeadline.IsZero() && !r.sleepDeadline.After(target)
		if timer == nil && !sleepDue {
			r.moveClockLocked(target)
			return
		}
		if timer != nil && (!sleepDue || !timer.when.After(r.sleepDeadline)) {
			r.moveClockLocked(timer.when)
			timer.stopped = true
			r.gen++
			r.parked = false
			f := timer.f
			r.mu.Unlock()
			f()
			r.mu.Lock()
			r.cond.Broadcast()
			continue
		}
		r.moveClockLocked(r.sleepDeadline)
		r.gen++
		r.parked = false
		r.cond.Broadcast()
	}
}

// waitParkedLocked blocks until the participant is parked with the current
// generation or has finished. Callers hold r.mu.
func (r *vRuntime) waitParkedLocked() {
	for !r.finished && !r.closed && (!r.parked || r.parkedGen != r.gen) {
		r.cond.Wait()
	}
}

func (r *vRuntime) nextTimerLocked(target time.Time) *vTimer {
	var next *vTimer
	for _, t := range r.timers {
		if t.stopped || t.when.After(target) {
			continue
		}
		if next == nil || t.when.Before(next.when) {
			next = t
		}
	}
	return next
}

func (r *vRuntime) nextDueLocked() (time.Time, bool) {
	var next time.Time
	found := false
	for _, t := range r.timers {
		if t.stopped {
			continue
		}
		if !found || t.when.Before(next) {
			next = t.when
			found = true
		}
	}
	if r.parked && !r.sleepDeadline.IsZero() && (!found || r.sleepDeadline.Before(next)) {
		next = r.sleepDeadline
		found = true
	}
	return next, found
}

func (r *vRuntime) moveClockLocked(target time.Time) {
	now := r.clock.Now()
	if target.After(now) {
		r.clock.Advance(target.Sub(now))
	}
}
