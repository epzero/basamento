package local

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/runtime"
)

func TestStartDuplicateRunIDSameTenant(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "noop", `{}`)
	_, err := f.rt.Start(tenantCtx("acme"), runtime.StartRequest{
		Flow:  runtime.FlowRef{Name: "noop"},
		RunID: "r1",
		Input: json.RawMessage(`{}`),
	})
	if codeOf(err) != "conflict" {
		t.Fatalf("duplicate Start = %v, want conflict", err)
	}
}

func TestStartDuplicateRunIDDifferentTenantsAllowed(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "shared", "noop", `{}`)
	f.start(t, "globex", "shared", "noop", `{}`)
	f.waitState(t, "acme", "shared", stateCompleted)
	f.waitState(t, "globex", "shared", stateCompleted)
}

func TestStartIdempotencyKeySameTenant(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var starts atomic.Int32
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		starts.Add(1)
		return json.RawMessage(`{}`), nil
	})
	req := runtime.StartRequest{
		Flow:           runtime.FlowRef{Name: "noop"},
		RunID:          "r1",
		Input:          json.RawMessage(`{}`),
		IdempotencyKey: "idem-1",
	}
	ref1, err := f.rt.Start(tenantCtx("acme"), req)
	if err != nil {
		t.Fatal(err)
	}
	ref2, err := f.rt.Start(tenantCtx("acme"), req)
	if err != nil {
		t.Fatal(err)
	}
	f.waitState(t, "acme", "r1", stateCompleted)
	if ref1 != ref2 || starts.Load() != 1 {
		t.Fatalf("refs %#v %#v starts %d, want one run", ref1, ref2, starts.Load())
	}
}

func TestStartIdempotencyKeyDifferentTenants(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var starts atomic.Int32
	f.flow("noop", func(runtime.DurableContext, json.RawMessage) (json.RawMessage, error) {
		starts.Add(1)
		return json.RawMessage(`{}`), nil
	})
	key := "shared-idem"
	for _, tenant := range []string{"acme", "globex"} {
		if _, err := f.rt.Start(tenantCtx(tenant), runtime.StartRequest{
			Flow:           runtime.FlowRef{Name: "noop"},
			RunID:          tenant + "-r",
			Input:          json.RawMessage(`{}`),
			IdempotencyKey: key,
		}); err != nil {
			t.Fatalf("Start(%s): %v", tenant, err)
		}
	}
	f.waitState(t, "acme", "acme-r", stateCompleted)
	f.waitState(t, "globex", "globex-r", stateCompleted)
	if starts.Load() != 2 {
		t.Fatalf("starts %d, want 2", starts.Load())
	}
}

