package local

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/epzero/basamento/runtime"
)

func TestStartAfterShutdownIsUnavailable(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	sleeping := make(chan struct{})
	f.flow("timer", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Sleep(time.Hour)
		close(sleeping)
		if err := wait.Get(nil); err != nil {
			return nil, err
		}
		return json.RawMessage(`{}`), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.rt.Serve(ctx) }()
	f.start(t, "acme", "r1", "timer", `{}`)
	waitOn(t, "the timer to register", sleeping)
	cancel()
	err := recvWithin(t, done, "Serve return")
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if got := f.state(t, "acme", "r1"); got != stateRunning {
		t.Fatalf("in-flight run after shutdown = %s, want running", got)
	}
	_, startErr := f.rt.Start(tenantCtx("acme"), runtime.StartRequest{Flow: runtime.FlowRef{Name: "timer"}, RunID: "r2"})
	if codeOf(startErr) != "unavailable" {
		t.Fatalf("Start after shutdown = %v, want unavailable", startErr)
	}
}

func TestShutdownCancelsStepsBeforeReturning(t *testing.T) {
	f := newFixture(t, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	entered := make(chan struct{})
	var delivered atomic.Bool
	var active atomic.Int32
	if err := f.rt.RegisterStep("render", func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		active.Add(1)
		defer active.Add(-1)
		close(entered)
		<-ctx.Done()
		delivered.Store(true)
		return nil, ctx.Err()
	}, runtime.StepDefaults{}); err != nil {
		t.Fatal(err)
	}
	f.flow("render", func(dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		return nil, dc.Step("render", nil, runtime.StepOptions{}).Get(&out)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = f.rt.Serve(ctx)
		close(done)
	}()
	f.start(t, "acme", "r1", "render", `{}`)
	waitOn(t, "the step to start", entered)
	cancel()
	waitOn(t, "the runtime to shut down", done)
	if !delivered.Load() {
		t.Fatal("Serve returned before the step observed cancellation")
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("%d steps still executing, want 0", got)
	}
	if got := f.state(t, "acme", "r1"); got != stateRunning {
		t.Fatalf("run after shutdown = %s, want running", got)
	}
}
