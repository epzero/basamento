package middleware

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

func TestFirstChunkTimeoutCancelsAndRetries(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		if a.Number == 1 {
			if err := p.sleep(ctx, time.Hour); err != nil {
				return model.Result{}, err
			}
		}
		emit(model.Event{Kind: model.EventText, Text: "Hi"})
		return succeeded("Hi", nil), nil
	}
	cfg := baseConfig(rt, p)
	cfg.Timeouts.FirstChunk = 10 * time.Second
	res, err := Call(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.Attempts) != 2 || p.invoked != 2 {
		t.Fatalf("attempts = %d, invocations = %d, want 2", len(res.Attempts), p.invoked)
	}
	first := res.Attempts[0]
	if first.Outcome != OutcomeCancelled || first.Code != core.CodeTimeout {
		t.Fatalf("attempt 1 = %+v, want a cancelled timeout", first)
	}
	if got := first.End.Sub(first.Start); got != 10*time.Second {
		t.Fatalf("attempt 1 lasted %v, want 10s", got)
	}
	if got := res.Attempts[1].Start.Sub(first.End); got != time.Second {
		t.Fatalf("wait after timeout = %v, want 1s", got)
	}
}

func TestFirstChunkTimeoutFailsWhenTheBudgetIsSpent(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, _ Attempt, _ func(model.Event)) (model.Result, error) {
		if err := p.sleep(ctx, time.Hour); err != nil {
			return model.Result{}, err
		}
		return succeeded("never", nil), nil
	}
	cfg := baseConfig(rt, p)
	cfg.Attempts = 2
	cfg.Timeouts.FirstChunk = 10 * time.Second
	res, err := Call(context.Background(), cfg)
	if got := codeOf(t, err); got != core.CodeTimeout {
		t.Fatalf("code = %s, want timeout", got)
	}
	if len(res.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(res.Attempts))
	}
}

func TestIdleTimeoutCancelsAQuietStream(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
		emit(model.Event{Kind: model.EventText, Text: "one"})
		if err := p.sleep(ctx, time.Hour); err != nil {
			return model.Result{}, err
		}
		return succeeded("unreachable", nil), nil
	}
	cfg := baseConfig(rt, p)
	cfg.Timeouts.Idle = 30 * time.Second
	res, err := Call(context.Background(), cfg)
	if got := codeOf(t, err); got != core.CodeTimeout {
		t.Fatalf("code = %s, want timeout", got)
	}
	first := res.Attempts[0]
	if first.Outcome != OutcomeCancelled || first.Events != 1 {
		t.Fatalf("attempt 1 = %+v, want cancelled after 1 event", first)
	}
	if got := first.End.Sub(first.Start); got != 30*time.Second {
		t.Fatalf("idle timeout fired after %v, want 30s", got)
	}
}

func TestDefaultTimeouts(t *testing.T) {
	tests := []struct {
		name    string
		run     func(ctx context.Context, p *scriptedProvider, emit func(model.Event)) (model.Result, error)
		elapsed time.Duration
		events  int
	}{
		{
			name: "silent stream times out at 30 seconds",
			run: func(ctx context.Context, p *scriptedProvider, _ func(model.Event)) (model.Result, error) {
				if err := p.sleep(ctx, time.Hour); err != nil {
					return model.Result{}, err
				}
				return succeeded("no", nil), nil
			},
			elapsed: 30 * time.Second,
		},
		{
			name: "quiet stream times out at 60 seconds",
			run: func(ctx context.Context, p *scriptedProvider, emit func(model.Event)) (model.Result, error) {
				emit(model.Event{Kind: model.EventText, Text: "one"})
				if err := p.sleep(ctx, time.Hour); err != nil {
					return model.Result{}, err
				}
				return succeeded("no", nil), nil
			},
			elapsed: 60 * time.Second,
			events:  1,
		},
		{
			name: "long stream stops at 10 minutes",
			run: func(ctx context.Context, p *scriptedProvider, emit func(model.Event)) (model.Result, error) {
				for {
					emit(model.Event{Kind: model.EventText, Text: "x"})
					if err := p.sleep(ctx, 5*time.Second); err != nil {
						return model.Result{}, err
					}
				}
			},
			elapsed: 10 * time.Minute,
			events:  120,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
			p := &scriptedProvider{rt: rt}
			p.run = func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error) {
				return tc.run(ctx, p, emit)
			}
			cfg := baseConfig(rt, p)
			cfg.Attempts = 1
			res, err := Call(context.Background(), cfg)
			if got := codeOf(t, err); got != core.CodeTimeout {
				t.Fatalf("code = %s, want timeout", got)
			}
			first := res.Attempts[0]
			if got := first.End.Sub(first.Start); got != tc.elapsed {
				t.Fatalf("timeout fired after %v, want %v", got, tc.elapsed)
			}
			if first.Events != tc.events {
				t.Fatalf("events = %d, want %d", first.Events, tc.events)
			}
		})
	}
}

func TestCallTotalTimeoutStopsAnInFlightStream(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, _ Attempt, emit func(model.Event)) (model.Result, error) {
		for {
			emit(model.Event{Kind: model.EventText, Text: "x"})
			if err := p.sleep(ctx, 5*time.Second); err != nil {
				return model.Result{}, err
			}
		}
	}
	cfg := baseConfig(rt, p)
	cfg.Attempts = 1
	cfg.Timeouts.CallTotal = 3 * time.Second
	res, err := Call(context.Background(), cfg)
	if got := codeOf(t, err); got != core.CodeTimeout {
		t.Fatalf("code = %s, want timeout", got)
	}
	first := res.Attempts[0]
	if first.Outcome != OutcomeCancelled {
		t.Fatalf("attempt 1 = %+v, want cancelled", first)
	}
	if got := first.End.Sub(first.Start); got != 3*time.Second {
		t.Fatalf("call total stopped the stream after %v, want 3s", got)
	}
	if first.Events != 1 {
		t.Fatalf("events = %d, want 1", first.Events)
	}
}

func TestAttemptTotalTimeoutStopsAStreamThatKeepsTalking(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	p := &scriptedProvider{rt: rt}
	p.run = func(ctx context.Context, p *scriptedProvider, _ Attempt, emit func(model.Event)) (model.Result, error) {
		for {
			emit(model.Event{Kind: model.EventText, Text: "x"})
			if err := p.sleep(ctx, 5*time.Second); err != nil {
				return model.Result{}, err
			}
		}
	}
	cfg := baseConfig(rt, p)
	cfg.Attempts = 1
	cfg.Timeouts.AttemptTotal = 28 * time.Second
	res, err := Call(context.Background(), cfg)
	if got := codeOf(t, err); got != core.CodeTimeout {
		t.Fatalf("code = %s, want timeout", got)
	}
	first := res.Attempts[0]
	if got := first.End.Sub(first.Start); got != 28*time.Second {
		t.Fatalf("attempt lasted %v, want 28s", got)
	}
	if first.Events != 6 {
		t.Fatalf("events = %d, want 6", first.Events)
	}
}
