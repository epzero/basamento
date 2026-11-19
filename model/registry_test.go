package model

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/epzero/basamento/core"
)

func testHandle(clientRef string) Handle {
	return Handle{Provider: "test", ID: clientRef, ClientRef: clientRef}
}

func TestRegistryStartIsIdempotent(t *testing.T) {
	reg := NewRegistry("test")
	first, existing := reg.Begin(t.Context(), "ref-1")
	if existing {
		t.Fatal("first begin reported an existing run")
	}
	second, existing := reg.Begin(t.Context(), "ref-1")
	if !existing {
		t.Fatal("second begin did not report the existing run")
	}
	if first != second {
		t.Fatal("second begin returned a different run")
	}
	if got := first.Handle(); got != testHandle("ref-1") {
		t.Fatalf("handle = %+v, want %+v", got, testHandle("ref-1"))
	}
}

func TestEventsAreOrderedAndReplayable(t *testing.T) {
	reg := NewRegistry("test")
	run, _ := reg.Begin(t.Context(), "ref-1")
	run.Publish(Event{Kind: EventText, Text: "a"})
	run.Publish(Event{Kind: EventText, Text: "b"})
	run.Publish(Event{Kind: EventText, Text: "c"})
	run.Succeed(Result{})

	all := drain(t, mustEvents(t, reg, run.Handle(), 0))
	if len(all) != 3 {
		t.Fatalf("got %d events, want 3", len(all))
	}
	for i, e := range all {
		if e.Seq != int64(i+1) {
			t.Fatalf("event %d has seq %d, want %d", i, e.Seq, i+1)
		}
	}
	rest := drain(t, mustEvents(t, reg, run.Handle(), 1))
	if len(rest) != 2 || rest[0].Text != "b" || rest[1].Text != "c" {
		t.Fatalf("events after 1 = %+v, want b then c", rest)
	}
}

func TestEventsChannelClosesAtTheEnd(t *testing.T) {
	reg := NewRegistry("test")
	run, _ := reg.Begin(t.Context(), "ref-1")
	run.Publish(Event{Kind: EventProgress, Progress: 0.5})
	run.Succeed(Result{})

	ch := mustEvents(t, reg, run.Handle(), 0)
	drain(t, ch)
	if _, ok := <-ch; ok {
		t.Fatal("events channel is still open after the invocation ended")
	}
}

func TestResultAfterSuccess(t *testing.T) {
	reg := NewRegistry("test")
	run, _ := reg.Begin(t.Context(), "ref-1")
	run.Publish(Event{Kind: EventText, Text: "hi"})
	run.Succeed(Result{Vectors: [][]float32{{1, 2}}})

	res, err := reg.Result(t.Context(), run.Handle())
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.State != StateSucceeded {
		t.Fatalf("state = %q, want succeeded", res.State)
	}
	if len(res.Vectors) != 1 || res.Vectors[0][1] != 2 {
		t.Fatalf("vectors = %+v", res.Vectors)
	}
}

func TestFailRecordsTheMappedCode(t *testing.T) {
	reg := NewRegistry("test")
	run, _ := reg.Begin(t.Context(), "ref-1")
	run.Fail(core.NewError(core.CodeRateLimited, "slow down", nil))

	res, err := reg.Result(t.Context(), run.Handle())
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.State != StateFailed {
		t.Fatalf("state = %q, want failed", res.State)
	}
	var failure Failure
	if err := json.Unmarshal(res.Error, &failure); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if failure.Code != core.CodeRateLimited {
		t.Fatalf("code = %q, want rate_limited", failure.Code)
	}
}

func TestCancelEndsTheInvocation(t *testing.T) {
	reg := NewRegistry("test")
	run, _ := reg.Begin(t.Context(), "ref-1")
	run.Publish(Event{Kind: EventProgress, Progress: 0.5})

	if err := reg.Cancel(t.Context(), run.Handle()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	res, err := reg.Result(t.Context(), run.Handle())
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.State != StateCancelled {
		t.Fatalf("state = %q, want cancelled", res.State)
	}
	if cause := run.Context().Err(); !errors.Is(cause, context.Canceled) {
		t.Fatalf("run context err = %v, want context.Canceled", cause)
	}
	select {
	case <-run.Done():
	default:
		t.Fatal("done channel is not closed after cancel")
	}
}

func TestCancelAfterEndIsANoop(t *testing.T) {
	reg := NewRegistry("test")
	run, _ := reg.Begin(t.Context(), "ref-1")
	run.Succeed(Result{})
	if err := reg.Cancel(t.Context(), run.Handle()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	res, _ := reg.Result(t.Context(), run.Handle())
	if res.State != StateSucceeded {
		t.Fatalf("state = %q, want succeeded", res.State)
	}
}

func TestLookupFindsTheInvocation(t *testing.T) {
	reg := NewRegistry("test")
	if h, err := reg.Lookup(t.Context(), "missing"); err != nil || h != nil {
		t.Fatalf("lookup missing = (%v, %v), want (nil, nil)", h, err)
	}
	run, _ := reg.Begin(t.Context(), "ref-1")
	h, err := reg.Lookup(t.Context(), "ref-1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if h == nil || *h != run.Handle() {
		t.Fatalf("lookup = %+v, want %+v", h, run.Handle())
	}
}

func TestUnknownHandle(t *testing.T) {
	reg := NewRegistry("test")
	if _, err := reg.Result(t.Context(), testHandle("missing")); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("result code = %q, want not_found", core.CodeOf(err))
	}
	if _, err := reg.Events(t.Context(), testHandle("missing"), 0); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("events code = %q, want not_found", core.CodeOf(err))
	}
	if err := reg.Cancel(t.Context(), Handle{}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("cancel code = %q, want invalid_argument", core.CodeOf(err))
	}
}

func mustEvents(t *testing.T, reg *Registry, h Handle, after int64) <-chan Event {
	t.Helper()
	ch, err := reg.Events(t.Context(), h, after)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return ch
}

func drain(t *testing.T, ch <-chan Event) []Event {
	t.Helper()
	var out []Event
	for e := range ch {
		out = append(out, e)
	}
	return out
}
