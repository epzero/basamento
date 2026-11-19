package node

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

// scriptInvoker is a provider whose Start and Lookup are scripted by the test.
// It records every start attempt and its client ref.
type scriptInvoker struct {
	start  func(attempt int, req model.Request) (model.Handle, error)
	lookup func(clientRef string) (*model.Handle, error)

	starts   int
	refs     []string
	lookups  int
	accepted map[string]model.Handle

	events []model.Event
	result model.Result
}

func newScriptInvoker() *scriptInvoker {
	return &scriptInvoker{accepted: map[string]model.Handle{}}
}

func (s *scriptInvoker) Describe(context.Context) ([]model.Descriptor, error) {
	return []model.Descriptor{{ID: "sim-video", Provider: "sim"}}, nil
}

func (s *scriptInvoker) Start(_ context.Context, req model.Request) (model.Handle, error) {
	s.starts++
	s.refs = append(s.refs, req.ClientRef)
	return s.start(s.starts, req)
}

func (s *scriptInvoker) Lookup(_ context.Context, clientRef string) (*model.Handle, error) {
	s.lookups++
	if s.lookup == nil {
		return nil, nil
	}
	return s.lookup(clientRef)
}

func (s *scriptInvoker) Events(context.Context, model.Handle, int64) (<-chan model.Event, error) {
	ch := make(chan model.Event, len(s.events))
	for _, event := range s.events {
		ch <- event
	}
	close(ch)
	return ch, nil
}

func (s *scriptInvoker) Result(context.Context, model.Handle) (model.Result, error) {
	if s.result.State == "" {
		return model.Result{State: model.StateSucceeded}, nil
	}
	return s.result, nil
}

func (s *scriptInvoker) Cancel(context.Context, model.Handle) error { return nil }

func retryable() error {
	return &core.Error{Code: core.CodeTimeout, Message: "no response", Retryable: true}
}

func TestStartRetriesWithTheSameClientRef(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(attempt int, req model.Request) (model.Handle, error) {
		if attempt == 1 {
			return model.Handle{}, retryable()
		}
		h := model.Handle{Provider: "sim", ID: req.ClientRef, ClientRef: req.ClientRef}
		inv.accepted[req.ClientRef] = h
		return h, nil
	}
	outcome, err := Starter{Invoker: inv}.Start(t.Context(), model.Request{ClientRef: "ref-1"})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2", outcome.Attempts)
	}
	if inv.starts != 2 || len(inv.accepted) != 1 {
		t.Fatalf("starts = %d, accepted = %d, want 2 and 1", inv.starts, len(inv.accepted))
	}
	if inv.refs[0] != "ref-1" || inv.refs[1] != "ref-1" {
		t.Fatalf("refs = %v, want the same ref twice", inv.refs)
	}
	if outcome.Ambiguous || outcome.Resolved {
		t.Fatalf("outcome = %+v, want a plain start", outcome)
	}
}

func TestStartResolvesAnAcceptedButUnansweredStartByLookup(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		inv.accepted[req.ClientRef] = model.Handle{Provider: "sim", ID: req.ClientRef, ClientRef: req.ClientRef}
		return model.Handle{}, retryable()
	}
	inv.lookup = func(ref string) (*model.Handle, error) {
		h := inv.accepted[ref]
		return &h, nil
	}
	outcome, err := Starter{Invoker: inv}.Start(t.Context(), model.Request{ClientRef: "ref-1"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.starts != 1 {
		t.Fatalf("starts = %d, want 1: an accepted start is never repeated", inv.starts)
	}
	if !outcome.Resolved || outcome.Ambiguous {
		t.Fatalf("outcome = %+v, want resolved", outcome)
	}
	if outcome.Handle.ClientRef != "ref-1" {
		t.Fatalf("handle = %+v, want ref-1", outcome.Handle)
	}
}

func TestStartWithoutLookupIsAmbiguous(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		inv.accepted[req.ClientRef] = model.Handle{Provider: "sim", ID: req.ClientRef, ClientRef: req.ClientRef}
		return model.Handle{}, retryable()
	}
	inv.lookup = func(string) (*model.Handle, error) {
		return nil, core.NewError(core.CodeUnsupported, "no lookup", nil)
	}
	outcome, err := Starter{Invoker: inv}.Start(t.Context(), model.Request{ClientRef: "ref-1"})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Ambiguous {
		t.Fatalf("outcome = %+v, want ambiguous", outcome)
	}
	if inv.starts != 1 {
		t.Fatalf("starts = %d, want 1: a blind second start would charge twice", inv.starts)
	}
}

func TestStartStopsOnANonRetryableError(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(int, model.Request) (model.Handle, error) {
		return model.Handle{}, core.NewError(core.CodeInvalidArgument, "bad parameters", nil)
	}
	_, err := Starter{Invoker: inv}.Start(t.Context(), model.Request{ClientRef: "ref-1"})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if inv.starts != 1 || inv.lookups != 0 {
		t.Fatalf("starts = %d, lookups = %d, want 1 and 0", inv.starts, inv.lookups)
	}
}

func TestStartStopsWhenAttemptsAreSpent(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(int, model.Request) (model.Handle, error) {
		return model.Handle{}, retryable()
	}
	_, err := Starter{Invoker: inv, MaxAttempts: 2}.Start(t.Context(), model.Request{ClientRef: "ref-1"})
	if core.CodeOf(err) != core.CodeTimeout {
		t.Fatalf("err = %v, want timeout", err)
	}
	if inv.starts != 2 {
		t.Fatalf("starts = %d, want 2", inv.starts)
	}
	if inv.refs[0] != inv.refs[1] {
		t.Fatalf("refs = %v, want the same ref", inv.refs)
	}
}

func TestStartNeedsAClientRef(t *testing.T) {
	t.Parallel()
	_, err := Starter{Invoker: newScriptInvoker()}.Start(t.Context(), model.Request{})
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

func TestStartNeedsAnInvoker(t *testing.T) {
	t.Parallel()
	_, err := Starter{}.Start(t.Context(), model.Request{ClientRef: "ref-1"})
	if core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("err = %v, want internal", err)
	}
}
