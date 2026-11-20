package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/structured"
)

// fakeInvoker records the requests Start receives and scripts its results.
type fakeInvoker struct {
	mu        sync.Mutex
	refs      []string
	cancels   []string
	eventsErr error
	resultErr error
	cancelErr error
	resultFor func(ref string) model.Result
}

func (f *fakeInvoker) Describe(context.Context) ([]model.Descriptor, error) { return nil, nil }

func (f *fakeInvoker) Start(_ context.Context, r model.Request) (model.Handle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refs = append(f.refs, r.ClientRef)
	return model.Handle{ID: r.ClientRef, ClientRef: r.ClientRef}, nil
}

func (f *fakeInvoker) Events(context.Context, model.Handle, int64) (<-chan model.Event, error) {
	if f.eventsErr != nil {
		return nil, f.eventsErr
	}
	ch := make(chan model.Event)
	close(ch)
	return ch, nil
}

func (f *fakeInvoker) Result(_ context.Context, h model.Handle) (model.Result, error) {
	if f.resultErr != nil {
		return model.Result{}, f.resultErr
	}
	return f.resultFor(h.ClientRef), nil
}

func (f *fakeInvoker) Cancel(_ context.Context, h model.Handle) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, h.ClientRef)
	return f.cancelErr
}

func (f *fakeInvoker) Lookup(context.Context, string) (*model.Handle, error) { return nil, nil }

func (f *fakeInvoker) started() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.refs...)
}

func (f *fakeInvoker) cancelled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cancels...)
}

func failedResult(t *testing.T, code core.Code, retryable bool, after time.Duration) model.Result {
	t.Helper()
	data, err := json.Marshal(model.Failure{Code: code, Message: string(code), Retryable: retryable, RetryAfter: after})
	if err != nil {
		t.Fatal(err)
	}
	return model.Result{State: model.StateFailed, Error: data}
}

func TestInvokerProviderSendsEachAttemptsOwnClientRef(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	inv := &fakeInvoker{}
	inv.resultFor = func(ref string) model.Result {
		if ref == "call-1" {
			return failedResult(t, core.CodeUnavailable, true, 0)
		}
		return succeeded("Hi", nil)
	}
	cfg := baseConfig(rt, InvokerProvider{Invoker: inv})
	res, err := Call(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(res.Attempts))
	}
	want := []string{"call-1", structured.ClientRef("call-1", 2)}
	got := inv.started()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Start refs = %v, want %v", got, want)
	}
}

func TestInvokerProviderKeepsRetryAfter(t *testing.T) {
	rt := newVirtualRuntime(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	inv := &fakeInvoker{}
	inv.resultFor = func(ref string) model.Result {
		if ref == "call-1" {
			return failedResult(t, core.CodeRateLimited, true, 5*time.Second)
		}
		return succeeded("Hi", nil)
	}
	res, err := Call(context.Background(), baseConfig(rt, InvokerProvider{Invoker: inv}))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if len(res.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(res.Attempts))
	}
	if got := res.Attempts[1].Start.Sub(res.Attempts[0].End); got != 5*time.Second {
		t.Fatalf("wait after Retry-After = %v, want 5s", got)
	}
}

func TestInvokerProviderCancelsWhenFollowingFails(t *testing.T) {
	tests := []struct {
		name string
		inv  *fakeInvoker
	}{
		{name: "events error", inv: &fakeInvoker{eventsErr: errors.New("stream broke")}},
		{name: "result error", inv: &fakeInvoker{resultErr: errors.New("result lost")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := InvokerProvider{Invoker: tc.inv}
			_, err := p.Attempt(context.Background(), Attempt{
				Number: 1, ClientRef: "call-1", Request: model.Request{ClientRef: "call-1"},
			}, func(model.Event) {})
			if err == nil {
				t.Fatal("Attempt succeeded, want an error")
			}
			got := tc.inv.cancelled()
			if len(got) != 1 || got[0] != "call-1" {
				t.Fatalf("Cancel calls = %v, want [call-1]", got)
			}
		})
	}
}

func TestInvokerProviderSurfacesCancelFailureAsNonRetryable(t *testing.T) {
	tests := []struct {
		name      string
		cancelErr error
		code      core.Code
	}{
		{name: "retryable vendor error becomes non-retryable", cancelErr: &core.Error{Code: core.CodeTimeout, Message: "vendor timeout", Retryable: true}, code: core.CodeTimeout},
		{name: "plain error maps to vendor error", cancelErr: errors.New("no answer"), code: core.CodeVendor},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inv := &fakeInvoker{eventsErr: errors.New("stream broke"), cancelErr: tc.cancelErr}
			_, err := (InvokerProvider{Invoker: inv}).Attempt(context.Background(), Attempt{
				Number: 1, ClientRef: "call-1", Request: model.Request{ClientRef: "call-1"},
			}, func(model.Event) {})
			if err == nil {
				t.Fatal("Attempt succeeded, want the cancel failure")
			}
			var ce *cancelError
			if !errors.As(err, &ce) {
				t.Fatalf("error = %T %v, want a cancelError", err, err)
			}
			if got := core.CodeOf(err); got != tc.code {
				t.Fatalf("code = %s, want %s", got, tc.code)
			}
			if core.IsRetryable(err) {
				t.Fatalf("cancel failure %v is retryable", err)
			}
			if got := inv.cancelled(); len(got) != 1 || got[0] != "call-1" {
				t.Fatalf("Cancel calls = %v, want [call-1]", got)
			}
		})
	}
}

// immediateTimerRuntime fires every timer as soon as it is scheduled.
type immediateTimerRuntime struct {
	now time.Time
}

func (r immediateTimerRuntime) Now() time.Time { return r.now }

func (immediateTimerRuntime) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func (immediateTimerRuntime) AfterFunc(_ time.Duration, f func()) Timer {
	f()
	return stoppedTimer{}
}

type stoppedTimer struct{}

func (stoppedTimer) Stop() {}

func TestCancelFailureStopsTheCallWithoutANewStart(t *testing.T) {
	now := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	inv := &fakeInvoker{cancelErr: core.NewError(core.CodeUnavailable, "cannot reach vendor", nil)}
	logs := &logCollector{}
	cfg := baseConfig(newVirtualRuntime(now), InvokerProvider{Invoker: inv})
	cfg.Runtime = immediateTimerRuntime{now: now}
	cfg.Attempts = 3
	cfg.CallLog = logs
	res, err := Call(context.Background(), cfg)
	if got := codeOf(t, err); got != core.CodeUnavailable {
		t.Fatalf("code = %s, want unavailable", got)
	}
	if core.IsRetryable(err) {
		t.Fatalf("cancel failure %v is retryable", err)
	}
	if got := inv.started(); len(got) != 1 {
		t.Fatalf("Start calls = %v, want 1 (no retry under a new client ref)", got)
	}
	if len(res.Attempts) != 1 || res.Attempts[0].Outcome != OutcomeFailed || res.Attempts[0].Code != core.CodeUnavailable {
		t.Fatalf("attempts = %+v, want one failed attempt with unavailable", res.Attempts)
	}
	if len(logs.entries) != 1 || logs.entries[0].State != OutcomeFailed {
		t.Fatalf("call log = %+v, want one failed entry", logs.entries)
	}
}
