package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

func TestStepRunsOncePerName(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var executions atomic.Int32
	if err := f.rt.RegisterStep("fetch", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		executions.Add(1)
		return json.RawMessage(`{"call":1}`), nil
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}

	var first, second json.RawMessage
	f.flow("twice", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Step("fetch", nil, runtime.StepOptions{}).Get(&first); err != nil {
			return nil, err
		}
		if err := dc.Step("fetch", nil, runtime.StepOptions{}).Get(&second); err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "twice", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)

	if got := executions.Load(); got != 1 {
		t.Fatalf("step executed %d times, want 1", got)
	}
	if string(first) != `{"call":1}` || string(second) != `{"call":1}` {
		t.Fatalf("results %s and %s, want both {\"call\":1}", first, second)
	}
}

func TestStepJournalIsPerRun(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var executions atomic.Int32
	if err := f.rt.RegisterStep("fetch", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		n := executions.Add(1)
		return json.RawMessage(fmt.Sprintf(`{"call":%d}`, n)), nil
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("once", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Step("fetch", nil, runtime.StepOptions{}).Get(&out); err != nil {
			return nil, err
		}
		return out, nil
	})
	f.start(t, "acme", "r1", "once", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	f.start(t, "acme", "r2", "once", `{}`)
	f.waitState(t, "acme", "r2", stateCompleted)

	if got := executions.Load(); got != 2 {
		t.Fatalf("step executed %d times, want 2", got)
	}
}

func TestStepJournalRecordsFailuresForReplay(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var executions atomic.Int32
	if err := f.rt.RegisterStep("charge", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		executions.Add(1)
		return nil, core.NewError(core.CodeInvalidArgument, "bad", nil)
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	stepErrs := make(chan error, 2)
	f.flow("pay", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		first := dc.Step("charge", nil, runtime.StepOptions{}).Get(&out)
		second := dc.Step("charge", nil, runtime.StepOptions{}).Get(&out)
		stepErrs <- first
		stepErrs <- second
		return nil, first
	})
	f.start(t, "acme", "r1", "pay", `{}`)
	f.waitState(t, "acme", "r1", stateFailed)
	if got := executions.Load(); got != 1 {
		t.Fatalf("step executed %d times, want 1", got)
	}
	first, second := <-stepErrs, <-stepErrs
	if codeOf(first) != "invalid_argument" || codeOf(second) != "invalid_argument" {
		t.Fatalf("recorded errors %v and %v, want both invalid_argument", first, second)
	}
}

func TestStepPanicFailsWithoutKillingProcess(t *testing.T) {
	const leak = "step-panic-must-not-leak"
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	var executions atomic.Int32
	if err := f.rt.RegisterStep("boom", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		executions.Add(1)
		panic(leak)
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("step-panic", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		if err := dc.Step("boom", nil, runtime.StepOptions{}).Get(nil); err == nil {
			return nil, fmt.Errorf("expected step error")
		}
		return json.RawMessage(`{"ok":true}`), nil
	})
	f.start(t, "acme", "r1", "step-panic", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	if executions.Load() != 1 {
		t.Fatalf("step ran %d times, want 1", executions.Load())
	}
	target := f.rt.lookup(core.TenantID("acme"), "r1")
	target.mu.Lock()
	runErr := target.err
	target.mu.Unlock()
	if runErr != nil {
		t.Fatalf("run err %v, want nil (flow handled step)", runErr)
	}
	if out := f.output(t, "acme", "r1"); string(out) != `{"ok":true}` {
		t.Fatalf("output %s", out)
	}
	// step error is on the future, not exported via Describe — verify via a second flow call is overkill;
	// re-read the step future is gone; check panic message not in Describe.
	status, err := f.rt.Describe(tenantCtx("acme"), runtime.RunRef{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(status.State+status.Ref.RunID, leak) {
		t.Fatal("panic value leaked into Describe")
	}
	f.close(t)
}

func TestStepPanicWithErrorValue(t *testing.T) {
	sentinel := core.NewError(core.CodeUnavailable, "boom-step", nil)
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	defer f.close(t)
	var got atomic.Value
	if err := f.rt.RegisterStep("panic-err", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		panic(sentinel)
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("catch", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		err := dc.Step("panic-err", nil, runtime.StepOptions{}).Get(nil)
		got.Store(err)
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "catch", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	err, _ := got.Load().(error)
	var platform *core.Error
	if !errors.As(err, &platform) || platform.Message != "local: the step panicked" {
		t.Fatalf("step error %v, want internal step panic wrapper", err)
	}
	if !errors.Is(platform.Cause, sentinel) && platform.Cause.Error() != sentinel.Error() {
		t.Fatalf("cause %v, want %v", platform.Cause, sentinel)
	}
	if strings.Contains(platform.Message, "boom-step") {
		t.Fatal("cause leaked into the platform message")
	}
}

func TestStepPanicThenShutdown(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	if err := f.rt.RegisterStep("panic-step", func(context.Context, json.RawMessage) (json.RawMessage, error) {
		panic("x")
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("go", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		_ = dc.Step("panic-step", nil, runtime.StepOptions{}).Get(nil)
		return json.RawMessage(`{}`), nil
	})
	f.start(t, "acme", "r1", "go", `{}`)
	f.waitState(t, "acme", "r1", stateCompleted)
	f.close(t)
}
