package model

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
)

// retentionClock is the fake clock the retention tests advance by hand.
func retentionClock() *coretest.FakeClock {
	return coretest.NewFakeClock(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
}

func newRetentionRegistry(t *testing.T, clock *coretest.FakeClock, window time.Duration, maxEnded int) *Registry {
	t.Helper()
	reg, err := NewRegistry("test", WithClock(clock), WithRetention(window, maxEnded))
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	return reg
}

func TestNewRegistryNeedsAClock(t *testing.T) {
	if _, err := NewRegistry("test"); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument", core.CodeOf(err))
	}
	if _, err := NewRegistry("test", WithClock(nil)); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("nil clock code = %q, want invalid_argument", core.CodeOf(err))
	}
}

func TestNewRegistryRejectsNegativeBounds(t *testing.T) {
	clock := retentionClock()
	if _, err := NewRegistry("test", WithClock(clock), WithRetention(-time.Second, 0)); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("negative window code = %q, want invalid_argument", core.CodeOf(err))
	}
	if _, err := NewRegistry("test", WithClock(clock), WithRetention(0, -1)); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("negative ended bound code = %q, want invalid_argument", core.CodeOf(err))
	}
}

func TestNewRegistryDefaultsAndZeroBounds(t *testing.T) {
	reg, err := NewRegistry("test", WithClock(retentionClock()))
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	if reg.retention != DefaultRetention || reg.maxEnded != DefaultMaxEnded {
		t.Fatalf("defaults = %v/%d, want %v/%d", reg.retention, reg.maxEnded, DefaultRetention, DefaultMaxEnded)
	}
	reg = newRetentionRegistry(t, retentionClock(), 30*time.Minute, 0)
	if reg.retention != 30*time.Minute || reg.maxEnded != DefaultMaxEnded {
		t.Fatalf("mixed bounds = %v/%d, want 30m/%d", reg.retention, reg.maxEnded, DefaultMaxEnded)
	}
}

func TestEndedInvocationStaysInsideItsWindow(t *testing.T) {
	clock := retentionClock()
	reg := newRetentionRegistry(t, clock, time.Hour, 0)
	run := mustBegin(t, reg, testCtx(t), "ref-1")
	run.Publish(Event{Kind: EventText, Text: "He"})
	run.Publish(Event{Kind: EventText, Text: "llo"})
	run.Succeed(Result{})

	clock.Advance(59 * time.Minute)
	h, err := reg.Lookup(testCtx(t), "ref-1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if h == nil || *h != run.Handle() {
		t.Fatalf("lookup = %v, want the invocation", h)
	}
	events := drain(t, mustEvents(t, reg, *h, 0))
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	res, err := reg.Result(testCtx(t), *h)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.State != StateSucceeded {
		t.Fatalf("state = %q, want succeeded", res.State)
	}
}

func TestEndedInvocationIsGoneAtItsWindow(t *testing.T) {
	clock := retentionClock()
	reg := newRetentionRegistry(t, clock, time.Hour, 0)
	run := mustBegin(t, reg, testCtx(t), "ref-1")
	run.Succeed(Result{})
	h := run.Handle()

	clock.Advance(time.Hour)
	got, err := reg.Lookup(testCtx(t), "ref-1")
	if err != nil || got != nil {
		t.Fatalf("lookup = (%v, %v), want (nil, nil)", got, err)
	}
	if _, err := reg.Events(testCtx(t), h, 0); !isNotFoundFor(err, "ref-1") {
		t.Fatalf("events = %v, want not_found naming ref-1", err)
	}
	if _, err := reg.Result(testCtx(t), h); !isNotFoundFor(err, "ref-1") {
		t.Fatalf("result = %v, want not_found naming ref-1", err)
	}
	if err := reg.Cancel(testCtx(t), h); !isNotFoundFor(err, "ref-1") {
		t.Fatalf("cancel = %v, want not_found naming ref-1", err)
	}
}

// isNotFoundFor reports whether err is not_found carrying client_ref in its
// details, exactly what an unknown handle answers.
func isNotFoundFor(err error, ref string) bool {
	var pe *core.Error
	if !errors.As(err, &pe) || pe.Code != core.CodeNotFound {
		return false
	}
	got, _ := pe.Details["client_ref"].(string)
	return got == ref
}

func TestStartAfterEvictionBeginsANewInvocation(t *testing.T) {
	clock := retentionClock()
	ctx := testCtx(t)
	for _, tc := range []struct {
		name string
		reg  *Registry
		age  time.Duration
	}{
		{name: "window", reg: newRetentionRegistry(t, clock, time.Hour, 0), age: time.Hour},
		{name: "ended bound", reg: newRetentionRegistry(t, clock, time.Hour, 1), age: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := mustBegin(t, tc.reg, ctx, "ref-1")
			first.Succeed(Result{})
			if tc.age > 0 {
				clock.Advance(tc.age)
			} else {
				// A second ended invocation pushes ref-1 past the bound of 1.
				mustBegin(t, tc.reg, ctx, "ref-2").Succeed(Result{})
			}
			second, existing, err := tc.reg.Begin(ctx, "ref-1")
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			if existing {
				t.Fatal("begin after eviction reported an existing run")
			}
			if second == first {
				t.Fatal("begin after eviction returned the evicted run")
			}
			h, err := tc.reg.Lookup(ctx, "ref-1")
			if err != nil || h == nil || *h != second.Handle() {
				t.Fatalf("lookup = (%v, %v), want the new invocation", h, err)
			}
		})
	}
}

func TestEvictedRefAnswersLikeANeverHeldOne(t *testing.T) {
	clock := retentionClock()
	reg := newRetentionRegistry(t, clock, time.Hour, 0)
	ctx := testCtx(t)
	run := mustBegin(t, reg, ctx, "ref-1")
	run.Succeed(Result{})
	clock.Advance(time.Hour)

	evicted := run.Handle()
	never := testHandle("ref-never")
	for _, h := range []Handle{evicted, never} {
		if _, err := reg.Events(ctx, h, 0); !isNotFoundFor(err, h.ClientRef) {
			t.Fatalf("events %q = %v, want not_found naming it", h.ClientRef, err)
		}
		if _, err := reg.Result(ctx, h); !isNotFoundFor(err, h.ClientRef) {
			t.Fatalf("result %q = %v, want not_found naming it", h.ClientRef, err)
		}
		if err := reg.Cancel(ctx, h); !isNotFoundFor(err, h.ClientRef) {
			t.Fatalf("cancel %q = %v, want not_found naming it", h.ClientRef, err)
		}
	}
	for _, ref := range []string{"ref-1", "ref-never"} {
		got, err := reg.Lookup(ctx, ref)
		if err != nil || got != nil {
			t.Fatalf("lookup %q = (%v, %v), want (nil, nil)", ref, got, err)
		}
	}
}

func TestEndedBoundEvictsTheOldestEndedFirst(t *testing.T) {
	clock := retentionClock()
	reg := newRetentionRegistry(t, clock, time.Hour, 2)
	ctx := testCtx(t)
	mustBegin(t, reg, ctx, "ref-1").Succeed(Result{})
	clock.Advance(time.Minute)
	mustBegin(t, reg, ctx, "ref-2").Succeed(Result{})
	clock.Advance(time.Minute)
	third := mustBegin(t, reg, ctx, "ref-3")
	third.Succeed(Result{})

	// The window has not passed, so only the ended bound can evict ref-1.
	for _, tc := range []struct {
		ref  string
		want bool
	}{
		{"ref-1", false},
		{"ref-2", true},
		{"ref-3", true},
	} {
		got, err := reg.Lookup(ctx, tc.ref)
		if err != nil {
			t.Fatalf("lookup %s: %v", tc.ref, err)
		}
		if (got != nil) != tc.want {
			t.Fatalf("lookup %s = %v, want found=%v", tc.ref, got, tc.want)
		}
	}
}

func TestEqualEndTimesKeepTheirEndOrder(t *testing.T) {
	clock := retentionClock()
	reg := newRetentionRegistry(t, clock, time.Hour, 1)
	ctx := testCtx(t)
	mustBegin(t, reg, ctx, "ref-1").Succeed(Result{})
	mustBegin(t, reg, ctx, "ref-2").Succeed(Result{})

	if got, err := reg.Lookup(ctx, "ref-2"); err != nil || got == nil {
		t.Fatalf("lookup ref-2 = (%v, %v), want the later invocation kept", got, err)
	}
	if got, err := reg.Lookup(ctx, "ref-1"); err != nil || got != nil {
		t.Fatalf("lookup ref-1 = (%v, %v), want the first ended evicted", got, err)
	}
}

func TestRunningInvocationIsNeverEvicted(t *testing.T) {
	clock := retentionClock()
	reg := newRetentionRegistry(t, clock, time.Hour, 2)
	ctx := testCtx(t)
	running := mustBegin(t, reg, ctx, "ref-1")

	clock.Advance(24 * time.Hour)
	for i := 1; i <= 3; i++ {
		mustBegin(t, reg, ctx, fmt.Sprintf("other-%d", i)).Succeed(Result{})
	}
	if got, err := reg.Lookup(ctx, "ref-1"); err != nil || got == nil {
		t.Fatalf("running invocation lookup = (%v, %v), want it retained", got, err)
	}

	running.Succeed(Result{})
	res, err := reg.Result(ctx, running.Handle())
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.State != StateSucceeded {
		t.Fatalf("state = %q, want succeeded", res.State)
	}
	if got, err := reg.Lookup(ctx, "ref-1"); err != nil || got == nil {
		t.Fatalf("ended invocation lookup = (%v, %v), want it retained inside its window", got, err)
	}
}

func TestConcurrentEndsAndSweeps(t *testing.T) {
	clock := retentionClock()
	reg := newRetentionRegistry(t, clock, time.Hour, 2)
	ctx := testCtx(t)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ref := fmt.Sprintf("ref-%d", i)
			run, _, err := reg.Begin(ctx, ref)
			if err != nil {
				t.Errorf("begin %s: %v", ref, err)
				return
			}
			run.Publish(Event{Kind: EventText, Text: ref})
			run.Succeed(Result{})
			if _, err := reg.Lookup(ctx, ref); err != nil {
				t.Errorf("lookup %s: %v", ref, err)
			}
		}(i)
	}
	wg.Wait()

	// Every invocation has ended, so one more sweep leaves at most the bound.
	if _, err := reg.Lookup(ctx, "never-started"); err != nil {
		t.Fatalf("final lookup: %v", err)
	}
	reg.mu.Lock()
	held := len(reg.ended) - reg.head
	reg.mu.Unlock()
	if held > reg.maxEnded {
		t.Fatalf("registry holds %d ended invocations, want at most %d", held, reg.maxEnded)
	}
}
