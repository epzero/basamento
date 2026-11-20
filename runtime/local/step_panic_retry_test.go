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

func TestStepErrorWithPanicMessageIsRetried(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var attempts atomic.Int32
	msg := "local: the step panicked"
	if err := f.rt.RegisterStep("looks-like-panic", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		n := attempts.Add(1)
		if n == 1 {
			return nil, &core.Error{Code: core.CodeUnavailable, Message: msg, Retryable: true}
		}
		return json.RawMessage(`{"ok":true}`), nil
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("retry", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Step("looks-like-panic", nil, runtime.StepOptions{MaxAttempts: 3}).Get(&out); err != nil {
			return nil, err
		}
		return out, nil
	})
	f.start(t, "acme", "r1", "retry", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	if attempts.Load() != 2 {
		t.Fatalf("step attempts = %d, want 2 (retried once)", attempts.Load())
	}
}
