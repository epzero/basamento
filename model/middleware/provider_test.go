package middleware

import (
	"context"
	"encoding/json"
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
	return nil
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
