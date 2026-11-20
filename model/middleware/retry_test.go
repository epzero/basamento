package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

func TestRetryWaitsDoubleFromOneSecond(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		if a.Number < 3 {
			return failed(coreErr(core.CodeUnavailable, true, 0))
		}
		emit(model.Event{Kind: model.EventText, Text: "Hi"})
		return succeeded("Hi", nil), nil
	}
	res, err := Call(context.Background(), baseConfig(rt, p))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.Attempts) != 3 || p.invoked != 3 {
		t.Fatalf("attempts = %d, invocations = %d, want 3 and 3", len(res.Attempts), p.invoked)
	}
	if got := res.Attempts[1].Start.Sub(res.Attempts[0].End); got != time.Second {
		t.Fatalf("wait before retry 1 = %v, want 1s", got)
	}
	if got := res.Attempts[2].Start.Sub(res.Attempts[1].End); got != 2*time.Second {
		t.Fatalf("wait before retry 2 = %v, want 2s", got)
	}
}

func TestJitterAddsToBaseWithoutChangingTheNextBase(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		if a.Number < 3 {
			return failed(coreErr(core.CodeUnavailable, true, 0))
		}
		return succeeded("Hi", nil), nil
	}
	cfg := baseConfig(rt, p)
	cfg.Rand = &fixedRand{draws: []int64{int64(500 * time.Millisecond), 0}}
	res, err := Call(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := res.Attempts[1].Start.Sub(res.Attempts[0].End); got != 1500*time.Millisecond {
		t.Fatalf("wait before retry 1 = %v, want 1.5s", got)
	}
	if got := res.Attempts[2].Start.Sub(res.Attempts[1].End); got != 2*time.Second {
		t.Fatalf("wait before retry 2 = %v, want 2s", got)
	}
}

func TestJitterIsSeededAndBounded(t *testing.T) {
	source := Seed(42)
	again := Seed(42)
	for retry := 0; retry < 6; retry++ {
		base := time.Second << retry
		for i := 0; i < 50; i++ {
			a := source.Int64n(int64(base/2) + 1)
			b := again.Int64n(int64(base/2) + 1)
			if a != b {
				t.Fatalf("same seed drew %d then %d", a, b)
			}
			if a < 0 || time.Duration(a) > base/2 {
				t.Fatalf("jitter %d outside [0, %v]", a, base/2)
			}
		}
	}
}

func TestRetryAfterLongerThanComputedWaitWins(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		if a.Number == 1 {
			return failed(coreErr(core.CodeRateLimited, true, 5*time.Second))
		}
		return succeeded("Hi", nil), nil
	}
	res, err := Call(context.Background(), baseConfig(rt, p))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := res.Attempts[1].Start.Sub(res.Attempts[0].End); got != 5*time.Second {
		t.Fatalf("wait after Retry-After = %v, want 5s", got)
	}
}

func TestRetryAfterShorterThanComputedWaitIsIgnored(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		if a.Number == 1 {
			return failed(coreErr(core.CodeRateLimited, true, 500*time.Millisecond))
		}
		return succeeded("Hi", nil), nil
	}
	res, err := Call(context.Background(), baseConfig(rt, p))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got := res.Attempts[1].Start.Sub(res.Attempts[0].End); got != time.Second {
		t.Fatalf("wait after short Retry-After = %v, want 1s", got)
	}
}

func TestNonRetryableErrorEndsTheCall(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	for _, code := range []core.Code{
		core.CodeVendor, core.CodeUnauthenticated, core.CodeInvalidArgument,
		core.CodeBudgetExceeded, core.CodeCancelled, core.CodeRateLimited,
	} {
		t.Run(string(code), func(t *testing.T) {
			p := &scriptedProvider{rt: rt}
			p.run = func(context.Context, *scriptedProvider, Attempt, func(model.Event)) (model.Result, error) {
				return failed(coreErr(code, false, 0))
			}
			res, err := Call(context.Background(), baseConfig(rt, p))
			if got := codeOf(t, err); got != code {
				t.Fatalf("code = %s, want %s", got, code)
			}
			if len(res.Attempts) != 1 || p.invoked != 1 {
				t.Fatalf("attempts = %d, invocations = %d, want 1", len(res.Attempts), p.invoked)
			}
		})
	}
}

func TestBudgetSpentFailsWithTheLastError(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(context.Context, *scriptedProvider, Attempt, func(model.Event)) (model.Result, error) {
		return failed(coreErr(core.CodeUnavailable, true, 0))
	}
	cfg := baseConfig(rt, p)
	cfg.Attempts = 2
	res, err := Call(context.Background(), cfg)
	if got := codeOf(t, err); got != core.CodeUnavailable {
		t.Fatalf("code = %s, want unavailable", got)
	}
	if len(res.Attempts) != 2 || p.invoked != 2 {
		t.Fatalf("attempts = %d, invocations = %d, want 2", len(res.Attempts), p.invoked)
	}
	if got := res.Attempts[1].Start.Sub(res.Attempts[0].End); got != time.Second {
		t.Fatalf("wait = %v, want 1s", got)
	}
}

func TestFailureAfterAnEventIsNotRetried(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(_ context.Context, _ *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		emit(model.Event{Kind: model.EventText, Text: "one"})
		emit(model.Event{Kind: model.EventText, Text: "two"})
		return failed(coreErr(core.CodeVendor, true, 0))
	}
	var events []model.Event
	cfg := baseConfig(rt, p)
	cfg.Emit = func(e model.Event) { events = append(events, e) }
	res, err := Call(context.Background(), cfg)
	if got := codeOf(t, err); got != core.CodeVendor {
		t.Fatalf("code = %s, want vendor_error", got)
	}
	if len(res.Attempts) != 1 || p.invoked != 1 {
		t.Fatalf("attempts = %d, invocations = %d, want 1", len(res.Attempts), p.invoked)
	}
	text := 0
	for _, e := range events {
		if e.Kind == model.EventReset {
			t.Fatalf("unexpected reset event: %+v", e)
		}
		if e.Kind == model.EventText {
			text++
		}
	}
	if text != 2 {
		t.Fatalf("text events = %d, want 2", text)
	}
}

func TestRetryStartsWithResetNamingTheAttempt(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(_ context.Context, _ *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		if a.Number == 1 {
			return failed(coreErr(core.CodeUnavailable, true, 0))
		}
		emit(model.Event{Kind: model.EventText, Text: "H"})
		emit(model.Event{Kind: model.EventText, Text: "i"})
		return succeeded("Hi", nil), nil
	}
	var events []model.Event
	cfg := baseConfig(rt, p)
	cfg.Emit = func(e model.Event) { events = append(events, e) }
	res, err := Call(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(res.Attempts))
	}
	if len(events) < 1 || events[0].Kind != model.EventReset || events[0].Attempt != 2 {
		t.Fatalf("first event = %+v, want reset for attempt 2", events[0])
	}
	text := 0
	for _, e := range events[1:] {
		if e.Kind == model.EventText {
			text++
			if e.Attempt != 2 {
				t.Fatalf("text event of attempt %d, want 2", e.Attempt)
			}
		}
	}
	if text != 2 {
		t.Fatalf("text events = %d, want 2", text)
	}
}

func TestCallTotalTimeoutEndsRetryingBeforeTheWait(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(context.Context, *scriptedProvider, Attempt, func(model.Event)) (model.Result, error) {
		return failed(coreErr(core.CodeUnavailable, true, 5*time.Second))
	}
	cfg := baseConfig(rt, p)
	cfg.Timeouts.CallTotal = 3 * time.Second
	res, err := Call(context.Background(), cfg)
	if got := codeOf(t, err); got != core.CodeTimeout {
		t.Fatalf("code = %s, want timeout", got)
	}
	if len(res.Attempts) != 1 || p.invoked != 1 {
		t.Fatalf("attempts = %d, invocations = %d, want 1", len(res.Attempts), p.invoked)
	}
}
