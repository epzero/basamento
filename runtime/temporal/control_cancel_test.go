package temporal

import (
	"context"
	"testing"

	"go.temporal.io/api/serviceerror"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestCancelHappyPathAndNotFound(t *testing.T) {
	fc := &fakeClient{}
	rt := testControlRuntime(t, fc)
	ctx := core.WithTenant(context.Background(), "acme")
	if err := rt.Cancel(ctx, runtime.RunRef{RunID: "r1"}, "reason-ignored"); err != nil {
		t.Fatal(err)
	}
	cancel := mustLastCall(fc, "cancel")
	if cancel.CancelID != "acme/r1" || cancel.CancelRunID != "" {
		t.Fatalf("Cancel workflow id: got id=%q run=%q want acme/r1 and empty run", cancel.CancelID, cancel.CancelRunID)
	}
	fc.cancelErr = &serviceerror.NotFound{}
	if err := rt.Cancel(ctx, runtime.RunRef{RunID: "r2"}, "x"); core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("not found %v", err)
	}
}
