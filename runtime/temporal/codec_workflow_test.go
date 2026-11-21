package temporal

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
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

func TestWorkflowCodecPolicyExpiredIOContextNilErrorReturnsNil(t *testing.T) {
	ioCtx := expiredCodecCtx()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic %v", r)
		}
	}()
	if ret := workflowCodecPolicy(true, nil, ioCtx, nil); ret != nil {
		t.Fatalf("unexpected return %v", ret)
	}
}

func TestWorkflowCodecPolicyNonCoreError(t *testing.T) {
	plain := errors.New("vendor broke")
	cases := []struct {
		name    string
		ioCtx   context.Context
		wantPan bool
	}{
		{"expired_io", expiredCodecCtx(), true},
		{"fresh_io", context.Background(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantPan {
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
			} else {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("unexpected panic %v", r)
					}
				}()
			}
			got := workflowCodecPolicy(true, nil, tc.ioCtx, plain)
			if !tc.wantPan {
				if core.CodeOf(got) != core.CodeInternal {
					t.Fatalf("got code %v want internal", core.CodeOf(got))
				}
				if strings.Contains(got.Error(), plain.Error()) {
					t.Fatalf("sanitized error %q leaks original text", got)
				}
			}
		})
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

func TestWorkflowCodecPolicyNonCoreErrorLogsBoundedText(t *testing.T) {
	const secret = "SECRET-STORE-TEXT"
	var logged string
	got := workflowCodecPolicy(true, func(pe *core.Error) { logged = pe.Message },
		context.Background(), errors.New(secret))
	if !strings.Contains(logged, secret) {
		t.Fatalf("log %q missing original text", logged)
	}
	if core.CodeOf(got) != core.CodeInternal {
		t.Fatalf("code %v want internal", core.CodeOf(got))
	}
	if strings.Contains(got.Error(), secret) {
		t.Fatalf("error %q leaks original text", got)
	}
}

func TestWorkflowCodecPolicyPermanentCoreErrorReturnsItself(t *testing.T) {
	pe := core.NewError(core.CodePermissionDenied, "denied", nil)
	got := workflowCodecPolicy(true, nil, context.Background(), pe)
	if got != pe {
		t.Fatalf("got %v want the same core error", got)
	}
}

func TestErrorFromPanicSanitizesNonErrorValue(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	const secret = "SECRET-PANIC-VALUE"
	got := errorFromPanic(secret, logger)
	if got == nil {
		t.Fatal("no error")
	}
	if strings.Contains(got.Error(), secret) {
		t.Fatalf("error %q leaks panic text", got)
	}
	if core.CodeOf(got) != core.CodeInternal {
		t.Fatalf("code %v want internal", core.CodeOf(got))
	}
	if !strings.Contains(buf.String(), secret) {
		t.Fatalf("log %q missing panic text", buf.String())
	}
}
