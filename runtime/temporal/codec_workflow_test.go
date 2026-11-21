package temporal

import (
	"context"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
)

func expiredCodecCtx() context.Context {
	past := time.Unix(0, 0)
	ctx, cancel := context.WithDeadline(context.Background(), past)
	cancel()
	return ctx
}

func TestWorkflowCodecPolicyCancelled(t *testing.T) {
	err := core.NewError(core.CodeCancelled, "write cancelled", nil)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		tf, ok := r.(taskFailure)
		if !ok || tf.code != core.CodeCancelled {
			t.Fatalf("panic %v", r)
		}
	}()
	if ret := workflowCodecPolicy(true, nil, context.Background(), err); ret != nil {
		t.Fatalf("unexpected return %v", ret)
	}
}

func TestWorkflowCodecPolicyDeadlineExceededCause(t *testing.T) {
	ioCtx := expiredCodecCtx()
	err := core.NewError(core.CodeCancelled, "write cancelled", nil)
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		tf, ok := r.(taskFailure)
		if !ok || tf.code != core.CodeTimeout {
			t.Fatalf("panic %v", r)
		}
	}()
	if ret := workflowCodecPolicy(true, nil, ioCtx, err); ret != nil {
		t.Fatalf("unexpected return %v", ret)
	}
}

func TestWorkflowCodecPolicyExpiredIOContextAlone(t *testing.T) {
	ioCtx := expiredCodecCtx()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		tf, ok := r.(taskFailure)
		if !ok || tf.code != core.CodeTimeout {
			t.Fatalf("panic %v", r)
		}
	}()
	if ret := workflowCodecPolicy(true, nil, ioCtx, nil); ret != nil {
		t.Fatalf("unexpected return %v", ret)
	}
}

func TestWorkflowCodecPolicyTimeoutFromDeadlineCause(t *testing.T) {
	err := &core.Error{Code: core.CodeTimeout, Message: "blob slow", Cause: context.DeadlineExceeded}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		tf, ok := r.(taskFailure)
		if !ok || tf.code != core.CodeTimeout {
			t.Fatalf("panic %v", r)
		}
	}()
	if ret := workflowCodecPolicy(true, nil, context.Background(), err); ret != nil {
		t.Fatalf("unexpected return %v", ret)
	}
}

func TestWorkflowCodecPolicyClientPathKeepsCancelledPermanent(t *testing.T) {
	err := core.NewError(core.CodeCancelled, "caller cancelled", nil)
	got := workflowCodecPolicy(false, nil, context.Background(), err)
	if core.CodeOf(got) != core.CodeCancelled {
		t.Fatalf("code %v", core.CodeOf(got))
	}
}

func TestCodecTransientCancelled(t *testing.T) {
	if !codecTransient(core.NewError(core.CodeCancelled, "x", nil)) {
		t.Fatal("cancelled is transient on workflow path")
	}
}
