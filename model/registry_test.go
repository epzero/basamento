package model

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/epzero/basamento/core"
)

func testHandle(clientRef string) Handle {
	return Handle{Provider: "test", ID: clientRef, ClientRef: clientRef}
}

func TestRegistryStartIsIdempotent(t *testing.T) {
	reg := NewRegistry("test")
	first, existing, err := reg.Begin(testCtx(t), "ref-1")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if existing {
		t.Fatal("first begin reported an existing run")
	}
	second, existing, err := reg.Begin(testCtx(t), "ref-1")
	if err != nil {
		t.Fatalf("second begin: %v", err)
	}
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
	run := mustBegin(t, reg, testCtx(t), "ref-1")
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
	run := mustBegin(t, reg, testCtx(t), "ref-1")
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
	run := mustBegin(t, reg, testCtx(t), "ref-1")
	run.Publish(Event{Kind: EventText, Text: "hi"})
	run.Succeed(Result{Vectors: [][]float32{{1, 2}}})

	res, err := reg.Result(testCtx(t), run.Handle())
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

func TestFailStripsRawVendorTextFromStoredError(t *testing.T) {
	const secret = "401: invalid key sk-secret"
	reg := NewRegistry("test")
	run := mustBegin(t, reg, testCtx(t), "ref-1")
	run.Fail(errors.New(secret))

	res, err := reg.Result(testCtx(t), run.Handle())
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.State != StateFailed {
		t.Fatalf("state = %q, want failed", res.State)
	}
	if bytes.Contains(res.Error, []byte("sk-secret")) {
		t.Fatalf("stored error leaks vendor text: %s", res.Error)
	}
	var failure Failure
	if err := json.Unmarshal(res.Error, &failure); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if failure.Code != core.CodeVendor || failure.Message != "vendor call failed" {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestFailRecordsPlatformCoreError(t *testing.T) {
	reg := NewRegistry("test")
	run := mustBegin(t, reg, testCtx(t), "ref-1")
	run.Fail(core.NewError(core.CodeRateLimited, "slow down", nil))

	res, _ := reg.Result(testCtx(t), run.Handle())
	var failure Failure
	if err := json.Unmarshal(res.Error, &failure); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if failure.Code != core.CodeRateLimited || failure.Message != "slow down" {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestFailMapsContextErrorsBeforePersisting(t *testing.T) {
	reg := NewRegistry("test")
	run := mustBegin(t, reg, testCtx(t), "ref-1")
	run.Fail(context.Canceled)

	res, _ := reg.Result(testCtx(t), run.Handle())
	var failure Failure
	if err := json.Unmarshal(res.Error, &failure); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if failure.Code != core.CodeCancelled {
		t.Fatalf("code = %q, want cancelled", failure.Code)
	}
}

func TestCancelEndsTheInvocation(t *testing.T) {
	reg := NewRegistry("test")
	run := mustBegin(t, reg, testCtx(t), "ref-1")
	run.Publish(Event{Kind: EventProgress, Progress: 0.5})

	if err := reg.Cancel(testCtx(t), run.Handle()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	res, err := reg.Result(testCtx(t), run.Handle())
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
	run := mustBegin(t, reg, testCtx(t), "ref-1")
	run.Succeed(Result{})
	if err := reg.Cancel(testCtx(t), run.Handle()); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	res, _ := reg.Result(testCtx(t), run.Handle())
	if res.State != StateSucceeded {
		t.Fatalf("state = %q, want succeeded", res.State)
	}
}

func TestLookupFindsTheInvocation(t *testing.T) {
	reg := NewRegistry("test")
	if h, err := reg.Lookup(testCtx(t), "missing"); err != nil || h != nil {
		t.Fatalf("lookup missing = (%v, %v), want (nil, nil)", h, err)
	}
	run := mustBegin(t, reg, testCtx(t), "ref-1")
	h, err := reg.Lookup(testCtx(t), "ref-1")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if h == nil || *h != run.Handle() {
		t.Fatalf("lookup = %+v, want %+v", h, run.Handle())
	}
}

func TestEventsReplayFromCursor(t *testing.T) {
	reg := NewRegistry("test")
	run := mustBegin(t, reg, testCtx(t), "ref-1")
	for i := 0; i < 5; i++ {
		run.Publish(Event{Kind: EventText, Text: fmt.Sprintf("%d", i)})
	}
	run.Succeed(Result{})

	from0 := drain(t, mustEvents(t, reg, run.Handle(), 0))
	if len(from0) != 5 {
		t.Fatalf("from 0: got %d events, want 5", len(from0))
	}
	from2 := drain(t, mustEvents(t, reg, run.Handle(), 2))
	if len(from2) != 3 || from2[0].Seq != 3 {
		t.Fatalf("from 2: %+v, want 3 events starting at seq 3", from2)
	}
	from4 := drain(t, mustEvents(t, reg, run.Handle(), 4))
	if len(from4) != 1 || from4[0].Text != "4" {
		t.Fatalf("from last seq: %+v", from4)
	}
}

func TestEventsNegativeCursorReplaysFromStart(t *testing.T) {
	reg := NewRegistry("test")
	run := mustBegin(t, reg, testCtx(t), "ref-1")
	run.Publish(Event{Kind: EventText, Text: "a"})
	run.Publish(Event{Kind: EventText, Text: "b"})
	run.Succeed(Result{})
	got := drain(t, mustEvents(t, reg, run.Handle(), -5))
	if len(got) != 2 {
		t.Fatalf("got %d events, want 2", len(got))
	}
}

func TestEventsCursorBeyondEndWaitsForLaterEvents(t *testing.T) {
	reg := NewRegistry("test")
	run := mustBegin(t, reg, testCtx(t), "ref-1")
	run.Publish(Event{Kind: EventText, Text: "first"})

	ch := mustEvents(t, reg, run.Handle(), 1)
	done := make(chan []Event, 1)
	go func() {
		done <- drain(t, ch)
	}()

	run.Publish(Event{Kind: EventText, Text: "second"})
	run.Succeed(Result{})

	select {
	case got := <-done:
		if len(got) != 1 || got[0].Text != "second" {
			t.Fatalf("events = %+v, want second after cursor caught up", got)
		}
	case <-t.Context().Done():
		t.Fatal("timed out waiting for events after large cursor")
	}
}

func TestEventsLongStreamLateAndEarlyFollowers(t *testing.T) {
	const n = 5000
	reg := NewRegistry("test")
	run := mustBegin(t, reg, testCtx(t), "ref-1")
	early := mustEvents(t, reg, run.Handle(), 0)
	var lateCh <-chan Event
	const lateStart = 2500
	for i := 0; i < n; i++ {
		run.Publish(Event{Kind: EventProgress, Progress: float64(i) / float64(n)})
		if i == lateStart-1 {
			lateCh = mustEvents(t, reg, run.Handle(), lateStart)
		}
	}
	run.Succeed(Result{})

	earlyBatch := drain(t, early)
	if len(earlyBatch) != n {
		t.Fatalf("early follower got %d events, want %d", len(earlyBatch), n)
	}
	for i, e := range earlyBatch {
		if e.Seq != int64(i+1) {
			t.Fatalf("early event %d seq %d, want %d", i, e.Seq, i+1)
		}
	}

	lateBatch := drain(t, lateCh)
	wantLate := n - lateStart
	if len(lateBatch) != wantLate {
		t.Fatalf("late follower got %d events, want %d", len(lateBatch), wantLate)
	}
	for i, e := range lateBatch {
		if e.Seq != int64(i+lateStart+1) {
			t.Fatalf("late event %d seq %d, want %d", i, e.Seq, i+lateStart+1)
		}
	}
}

func TestUnknownHandle(t *testing.T) {
	reg := NewRegistry("test")
	if _, err := reg.Result(testCtx(t), testHandle("missing")); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("result code = %q, want not_found", core.CodeOf(err))
	}
	if _, err := reg.Events(testCtx(t), testHandle("missing"), 0); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("events code = %q, want not_found", core.CodeOf(err))
	}
	if err := reg.Cancel(testCtx(t), Handle{}); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("cancel code = %q, want invalid_argument", core.CodeOf(err))
	}
}

func mustEvents(t *testing.T, reg *Registry, h Handle, after int64) <-chan Event {
	return mustEventsCtx(t, reg, testCtx(t), h, after)
}

func mustEventsCtx(t *testing.T, reg *Registry, ctx context.Context, h Handle, after int64) <-chan Event {
	t.Helper()
	ch, err := reg.Events(ctx, h, after)
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
