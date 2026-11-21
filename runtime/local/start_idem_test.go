package local

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/runtime"
)

func TestStartConflictDoesNotStoreIdempotencyKey(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("block", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		<-dc.Context().Done()
		return nil, dc.Context().Err()
	})
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "block", `{}`)
	_, err := f.rt.Start(tenantCtx("acme"), runtime.StartRequest{
		Flow:           runtime.FlowRef{Name: "block"},
		RunID:          "r1",
		Input:          json.RawMessage(`{}`),
		IdempotencyKey: "k-failed",
	})
	if codeOf(err) != "conflict" {
		t.Fatalf("duplicate Start = %v, want conflict", err)
	}
	ref, err := f.rt.Start(tenantCtx("acme"), runtime.StartRequest{
		Flow:           runtime.FlowRef{Name: "noop"},
		RunID:          "r2",
		Input:          json.RawMessage(`{}`),
		IdempotencyKey: "k-failed",
	})
	if err != nil {
		t.Fatalf("Start with idem after failed conflict: %v", err)
	}
	if ref.RunID != "r2" {
		t.Fatalf("ref %q, want r2", ref.RunID)
	}
	f.waitState(t, "acme", "r2", stateCompleted)
}

func TestStartIdempotentNoSecondRunOrID(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var n atomic.Int32
	f.flow("count", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		n.Add(1)
		return json.RawMessage(`{}`), nil
	})
	req := runtime.StartRequest{
		Flow:           runtime.FlowRef{Name: "count"},
		RunID:          "r1",
		Input:          json.RawMessage(`{}`),
		IdempotencyKey: "k1",
	}
	ref1, err := f.rt.Start(tenantCtx("acme"), req)
	if err != nil {
		t.Fatal(err)
	}
	ref2, err := f.rt.Start(tenantCtx("acme"), runtime.StartRequest{
		Flow:           runtime.FlowRef{Name: "count"},
		Input:          json.RawMessage(`{}`),
		IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ref1 != ref2 {
		t.Fatalf("refs differ: %#v vs %#v", ref1, ref2)
	}
	f.waitState(t, "acme", "r1", stateCompleted)
	if n.Load() != 1 {
		t.Fatalf("flow ran %d times, want 1", n.Load())
	}
	runs, _, err := f.rt.List(tenantCtx("acme"), runtime.RunQuery{Flow: "count"})
	if err != nil || len(runs) != 1 {
		t.Fatalf("List = %d runs, %v", len(runs), err)
	}
}

func TestStartEmptyIdempotencyKeyNeverDedupes(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var n atomic.Int32
	f.flow("work", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		n.Add(1)
		return json.RawMessage(`{}`), nil
	})
	for _, id := range []string{"r1", "r2"} {
		if _, err := f.rt.Start(tenantCtx("acme"), runtime.StartRequest{
			Flow:  runtime.FlowRef{Name: "work"},
			RunID: id,
			Input: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("Start(%s): %v", id, err)
		}
	}
	f.waitState(t, "acme", "r1", stateCompleted)
	f.waitState(t, "acme", "r2", stateCompleted)
	if n.Load() != 2 {
		t.Fatalf("flow ran %d times, want 2", n.Load())
	}
}
