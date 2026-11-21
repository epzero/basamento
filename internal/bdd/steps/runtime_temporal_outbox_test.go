package steps

import (
	"context"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestMemOutboxAppendIsIdempotentBySeq(t *testing.T) {
	o := newMemOutbox()
	ctx := core.WithTenant(context.Background(), "acme")
	run := runtime.RunRef{RunID: "r1", TenantID: "acme"}
	o.wantRun = run
	ev := runtime.ProgressEvent{Type: "a"}
	if err := o.Append(ctx, run, 1, ev); err != nil {
		t.Fatal(err)
	}
	if err := o.Append(ctx, run, 1, ev); err != nil {
		t.Fatalf("duplicate append: %v", err)
	}
	events := o.eventsInSeqOrder()
	if len(events) != 1 || events[0].Type != "a" {
		t.Fatalf("events %+v want one a", events)
	}
}

func TestMemOutboxRejectsSeqReusedForDifferentEvent(t *testing.T) {
	o := newMemOutbox()
	ctx := core.WithTenant(context.Background(), "acme")
	run := runtime.RunRef{RunID: "r1", TenantID: "acme"}
	o.wantRun = run
	if err := o.Append(ctx, run, 1, runtime.ProgressEvent{Type: "a"}); err != nil {
		t.Fatal(err)
	}
	err := o.Append(ctx, run, 2, runtime.ProgressEvent{Type: "b"})
	if err != nil {
		t.Fatal(err)
	}
	err = o.Append(ctx, run, 1, runtime.ProgressEvent{Type: "c"})
	if core.CodeOf(err) != core.CodeConflict {
		t.Fatalf("reused seq error %v, want conflict", err)
	}
}

func TestMemOutboxAllowsSkippedSeq(t *testing.T) {
	o := newMemOutbox()
	ctx := core.WithTenant(context.Background(), "acme")
	run := runtime.RunRef{RunID: "r1", TenantID: "acme"}
	o.wantRun = run
	if err := o.Append(ctx, run, 1, runtime.ProgressEvent{Type: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := o.Append(ctx, run, 3, runtime.ProgressEvent{Type: "c"}); err != nil {
		t.Fatalf("skipped seq rejected: %v", err)
	}
	events := o.eventsInSeqOrder()
	if len(events) != 2 || events[0].Type != "a" || events[1].Type != "c" {
		t.Fatalf("events %+v want [a c]", events)
	}
}
