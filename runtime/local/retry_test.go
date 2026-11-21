package local

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestRetryPolicies(t *testing.T) {
	t.Run("non-retryable error stops after one attempt", func(t *testing.T) {
		f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
		defer f.close(t)
		var attempts atomic.Int32
		if err := f.rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
			attempts.Add(1)
			return nil, core.NewError(core.CodeInvalidArgument, "bad", nil)
		}, runtime.StepDefaults{}); err != nil {
			t.Fatal(err)
		}
		f.flow("pay", payFlow)
		f.start(t, "acme", "r1", "pay", `{}`)
		f.waitState(t, "acme", "r1", stateFailed)
		if got := attempts.Load(); got != 1 {
			t.Fatalf("attempts = %d, want 1", got)
		}
	})

	t.Run("listed code stops retries even when marked retryable", func(t *testing.T) {
		f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
		defer f.close(t)
		var attempts atomic.Int32
		if err := f.rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
			attempts.Add(1)
			return nil, &core.Error{Code: core.CodeRateLimited, Message: "slow", Retryable: true}
		}, runtime.StepDefaults{}); err != nil {
			t.Fatal(err)
		}
		f.flow("pay", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			var out json.RawMessage
			return nil, dc.Step("charge", nil, runtime.StepOptions{NonRetryable: []string{"rate_limited"}}).Get(&out)
		})
		f.start(t, "acme", "r1", "pay", `{}`)
		f.waitState(t, "acme", "r1", stateFailed)
		if got := attempts.Load(); got != 1 {
			t.Fatalf("attempts = %d, want 1", got)
		}
	})

	t.Run("attempt limit keeps the last error", func(t *testing.T) {
		f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
		defer f.close(t)
		var attempts atomic.Int32
		if err := f.rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
			n := attempts.Add(1)
			if n == 1 {
				return nil, &core.Error{Code: core.CodeRateLimited, Message: "slow", Retryable: true}
			}
			return nil, &core.Error{Code: core.CodeVendor, Message: "vendor", Retryable: true}
		}, runtime.StepDefaults{}); err != nil {
			t.Fatal(err)
		}
		stepErrs := make(chan error, 1)
		f.flow("pay", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
			var out json.RawMessage
			err := dc.Step("charge", nil, runtime.StepOptions{MaxAttempts: 2}).Get(&out)
			stepErrs <- err
			return nil, err
		})
		f.start(t, "acme", "r1", "pay", `{}`)
		f.waitState(t, "acme", "r1", stateFailed)
		if got := attempts.Load(); got != 2 {
			t.Fatalf("attempts = %d, want 2", got)
		}
		if err := <-stepErrs; codeOf(err) != "vendor_error" {
			t.Fatalf("step error = %v, want vendor_error", err)
		}
		if f.clock.Now() != time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC) {
			t.Fatal("a retry advanced the clock")
		}
	})

	t.Run("default limit is three", func(t *testing.T) {
		f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
		defer f.close(t)
		var attempts atomic.Int32
		if err := f.rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
			attempts.Add(1)
			return nil, &core.Error{Code: core.CodeVendor, Message: "vendor", Retryable: true}
		}, runtime.StepDefaults{}); err != nil {
			t.Fatal(err)
		}
		f.flow("pay", payFlow)
		f.start(t, "acme", "r1", "pay", `{}`)
		f.waitState(t, "acme", "r1", stateFailed)
		if got := attempts.Load(); got != 3 {
			t.Fatalf("attempts = %d, want 3", got)
		}
	})

	t.Run("a successful attempt ends the step", func(t *testing.T) {
		f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
		defer f.close(t)
		var attempts atomic.Int32
		if err := f.rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
			if attempts.Add(1) == 1 {
				return nil, &core.Error{Code: core.CodeVendor, Message: "vendor", Retryable: true}
			}
			return json.RawMessage(`{"charged":true}`), nil
		}, runtime.StepDefaults{}); err != nil {
			t.Fatal(err)
		}
		f.flow("pay", payFlow)
		f.start(t, "acme", "r1", "pay", `{}`)
		f.waitState(t, "acme", "r1", stateCompleted)
		if got := attempts.Load(); got != 2 {
			t.Fatalf("attempts = %d, want 2", got)
		}
		if out := f.output(t, "acme", "r1"); string(out) != `{"charged":true}` {
			t.Fatalf("output %s, want {\"charged\":true}", out)
		}
	})
}

func payFlow(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
	var out json.RawMessage
	if err := dc.Step("charge", nil, runtime.StepOptions{}).Get(&out); err != nil {
		return nil, err
	}
	return out, nil
}
