package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestResolveHappyPath(t *testing.T) {
	fc := &fakeClient{updateHandle: fakeUpdateHandle{}}
	rt := testControlRuntime(t, fc)
	ctx := core.WithTenant(context.Background(), "acme")
	payload := json.RawMessage(`{"ok":true}`)
	if err := rt.Resolve(ctx, runtime.RunRef{RunID: "r1"}, "gate1", payload); err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	opts := mustLastCall(fc, "update").UpdateOpts
	if opts.WorkflowID != "acme/r1" || opts.UpdateName != ResolveGateUpdate {
		t.Fatalf("update opts: got WorkflowID=%q UpdateName=%q", opts.WorkflowID, opts.UpdateName)
	}
	if opts.WaitForStage != client.WorkflowUpdateStageCompleted {
		t.Fatalf("WaitForStage: got %v want WorkflowUpdateStageCompleted", opts.WaitForStage)
	}
	res := opts.Args[0].(GateResolution)
	if res.Gate != "gate1" || string(res.Payload) != string(payload) {
		t.Fatalf("GateResolution: got gate=%q payload=%s", res.Gate, res.Payload)
	}
}

func TestResolveValidatorCodeMapping(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	for _, code := range []core.Code{core.CodeNotFound, core.CodeConflict, core.CodeUnavailable} {
		t.Run(string(code), func(t *testing.T) {
			fc := &fakeClient{updateHandle: fakeUpdateHandle{getErr: temporal.NewNonRetryableApplicationError("msg", string(code), nil)}}
			rt := testControlRuntime(t, fc)
			err := rt.Resolve(ctx, runtime.RunRef{RunID: "r1"}, "g", nil)
			var pe *core.Error
			if !errors.As(err, &pe) || pe.Code != code {
				t.Fatalf("expected code %s, got %v", code, err)
			}
		})
	}
	t.Run("internal-on-unknown-validator", func(t *testing.T) {
		fc := &fakeClient{updateHandle: fakeUpdateHandle{getErr: temporal.NewNonRetryableApplicationError("bad", "weird", nil)}}
		err := testControlRuntime(t, fc).Resolve(ctx, runtime.RunRef{RunID: "r1"}, "g", nil)
		if core.CodeOf(err) != core.CodeInternal {
			t.Fatalf("expected internal, got %v", err)
		}
	})
}

func TestResolveNotFoundFromClient(t *testing.T) {
	ctx := core.WithTenant(context.Background(), "acme")
	fc := &fakeClient{updateErr: &serviceerror.NotFound{}}
	err := testControlRuntime(t, fc).Resolve(ctx, runtime.RunRef{RunID: "r1"}, "g", nil)
	if core.CodeOf(err) != core.CodeNotFound {
		t.Fatalf("expected not_found, got %v", err)
	}
}
