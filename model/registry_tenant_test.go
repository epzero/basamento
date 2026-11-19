package model

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestRegistrySameRefIsolatedPerTenant(t *testing.T) {
	reg := NewRegistry("test")
	ctxA := tenantCtx("tenant-a")
	ctxB := tenantCtx("tenant-b")
	runA := mustBegin(t, reg, ctxA, "shared-ref")
	runB := mustBegin(t, reg, ctxB, "shared-ref")
	if runA == runB {
		t.Fatal("expected distinct runs for the same client ref in different tenants")
	}
	runA.Publish(Event{Kind: EventText, Text: "from-a"})
	runB.Publish(Event{Kind: EventText, Text: "from-b"})
	runA.Succeed(Result{Vectors: [][]float32{{1}}})
	runB.Succeed(Result{Vectors: [][]float32{{2}}})

	resA, err := reg.Result(ctxA, runA.Handle())
	if err != nil || resA.Vectors[0][0] != 1 {
		t.Fatalf("tenant A result = %+v, err %v", resA, err)
	}
	resB, err := reg.Result(ctxB, runB.Handle())
	if err != nil || resB.Vectors[0][0] != 2 {
		t.Fatalf("tenant B result = %+v, err %v", resB, err)
	}
	eventsA := drain(t, mustEventsCtx(t, reg, ctxA, runA.Handle(), 0))
	if len(eventsA) != 1 || eventsA[0].Text != "from-a" {
		t.Fatalf("tenant A events = %+v", eventsA)
	}
}

func TestRegistryCrossTenantHandleIsNotFound(t *testing.T) {
	reg := NewRegistry("test")
	ctxA := tenantCtx("tenant-a")
	ctxB := tenantCtx("tenant-b")
	run := mustBegin(t, reg, ctxA, "ref-x")
	run.Publish(Event{Kind: EventProgress, Progress: 0.5})
	h := run.Handle()

	if _, err := reg.Events(ctxB, h, 0); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("events code = %q, want not_found", core.CodeOf(err))
	}
	if _, err := reg.Result(ctxB, h); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("result code = %q, want not_found", core.CodeOf(err))
	}
	if err := reg.Cancel(ctxB, h); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("cancel code = %q, want not_found", core.CodeOf(err))
	}
	if got, err := reg.Lookup(ctxB, "ref-x"); err != nil || got != nil {
		t.Fatalf("lookup under B = (%v, %v), want (nil, nil)", got, err)
	}
	// Tenant A's invocation is untouched.
	if err := run.Context().Err(); err != nil {
		t.Fatalf("run context ended: %v", err)
	}
	select {
	case <-run.Done():
		t.Fatal("invocation cancelled by cross-tenant access")
	default:
	}
}

func TestRegistryRequiresTenant(t *testing.T) {
	reg := NewRegistry("test")
	ctx := context.Background()
	if _, _, err := reg.Begin(ctx, "ref"); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("begin code = %q, want invalid_argument", core.CodeOf(err))
	}
	h := testHandle("ref")
	if _, err := reg.Events(ctx, h, 0); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("events code = %q", core.CodeOf(err))
	}
	if _, err := reg.Result(ctx, h); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("result code = %q", core.CodeOf(err))
	}
	if err := reg.Cancel(ctx, h); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("cancel code = %q", core.CodeOf(err))
	}
	if _, err := reg.Lookup(ctx, "ref"); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("lookup code = %q", core.CodeOf(err))
	}
}

func TestEmbeddingRequiresTenantOnStart(t *testing.T) {
	adapter, _ := NewEmbedding(embedConfig(&fakeEmbedder{vecs: [][]float64{{1}}}))
	req := Request{Model: "sim-embed", Op: CapEmbed, Texts: []string{"x"}, ClientRef: "r"}
	if _, err := adapter.Start(context.Background(), req); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code = %q, want invalid_argument", core.CodeOf(err))
	}
}

func TestEmbeddingTenantIsolation(t *testing.T) {
	f := &fakeEmbedder{vecs: [][]float64{{1}}}
	adapter, _ := NewEmbedding(embedConfig(f))
	req := Request{Model: "sim-embed", Op: CapEmbed, Texts: []string{"x"}, ClientRef: "shared"}
	hA, err := adapter.Start(tenantCtx("a"), req)
	if err != nil {
		t.Fatalf("start A: %v", err)
	}
	if _, err := adapter.Result(tenantCtx("b"), hA); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("cross-tenant result code = %q", core.CodeOf(err))
	}
	if err := adapter.Cancel(tenantCtx("b"), hA); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("cross-tenant cancel code = %q", core.CodeOf(err))
	}
	got, err := adapter.Lookup(tenantCtx("b"), "shared")
	if err != nil || got != nil {
		t.Fatalf("lookup B = (%v, %v), want (nil, nil)", got, err)
	}
}
