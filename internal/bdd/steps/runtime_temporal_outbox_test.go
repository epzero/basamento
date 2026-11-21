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
	types, err := o.eventTypesDense()
	if err != nil {
		t.Fatal(err)
	}
	if len(types) != 1 || types[0] != "a" {
		t.Fatalf("types %v want one a", types)
	}
}
