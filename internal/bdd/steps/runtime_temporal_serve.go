package steps

import (
	"context"
	"errors"
	"fmt"

	"github.com/cucumber/godog"
	enumspb "go.temporal.io/api/enums/v1"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime/temporal"
)

func registerTemporalServe(sc *godog.ScenarioContext) {
	sc.Step(`^a simulated Temporal server version "([^"]*)"$`, func(ctx context.Context, version string) error {
		w := temporalW(ctx)
		if w.stub == nil {
			return fmt.Errorf("no temporal test server")
		}
		w.stub.SetVersion(version)
		return nil
	})
	sc.Step(`^the temporal runtime starts against the Temporal test server$`, func(ctx context.Context) error {
		return temporalW(ctx).beginServe(ctx)
	})
	sc.Step(`^the temporal runtime starts against that server$`, func(ctx context.Context) error {
		return temporalW(ctx).beginServe(ctx)
	})
	sc.Step(`^the runtime is serving$`, func(ctx context.Context) error {
		w := temporalW(ctx)
		// Success leaves serve.done for After; Serve ending first drains done here so After does not hang.
		pollers, serveErr, err := w.awaitServe(ctx)
		if err != nil {
			return err
		}
		if !pollers {
			return formatServeEndedBeforePollers(serveErr)
		}
		attrs := w.stub.SearchAttributes()
		if attrs[temporal.FlowSearchAttribute] != enumspb.INDEXED_VALUE_TYPE_KEYWORD {
			return fmt.Errorf("BasamentoFlow is not registered as Keyword on the stub")
		}
		return nil
	})
	sc.Step(`^starting the runtime fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := temporalW(ctx)
		pollers, _, err := w.awaitServe(ctx)
		if err != nil {
			return err
		}
		if pollers {
			return fmt.Errorf("the runtime is serving, want it to fail with code %q", code)
		}
		return temporalControlLastError(w, code)
	})
}

func formatServeEndedBeforePollers(err error) error {
	prefix := "Serve ended before the worker started"
	if err == nil {
		return errors.New(prefix + ": <nil>")
	}
	return fmt.Errorf("%s: %s (%w)", prefix, core.CodeOf(err), err)
}

// awaitServe races poller arrival on the stub against Serve's return on done. When Serve
// finishes first, done is drained so After does not block on an empty channel after a failed Then.
func (w *temporalWorld) awaitServe(ctx context.Context) (pollersReady bool, serveErr error, err error) {
	w.mu.Lock()
	done := w.serve.done
	stub := w.stub
	w.mu.Unlock()
	if done == nil {
		return false, nil, fmt.Errorf("serve was not started")
	}
	if stub == nil {
		return false, nil, fmt.Errorf("no temporal test server")
	}

	pollCtx, pollCancel := context.WithCancel(ctx)
	defer pollCancel()
	pollReady := make(chan struct{}, 1)
	pollFail := make(chan error, 1)
	go func() {
		pollErr := stub.WaitForPolls(pollCtx, "workflow", "activity")
		if pollErr == nil {
			pollReady <- struct{}{}
			return
		}
		if errors.Is(pollErr, context.Canceled) || errors.Is(pollErr, context.DeadlineExceeded) {
			return
		}
		select {
		case pollFail <- pollErr:
		default:
		}
	}()

	for {
		select {
		case <-ctx.Done():
			pollCancel()
			return false, nil, ctx.Err()
		case <-pollReady:
			pollCancel()
			return true, nil, nil
		case pollErr := <-pollFail:
			return false, nil, pollErr
		case serveErr = <-done:
			pollCancel()
			w.mu.Lock()
			w.control.lastErr = serveErr
			w.serve.done = nil
			w.mu.Unlock()
			return false, serveErr, nil
		}
	}
}

func (w *temporalWorld) beginServe(ctx context.Context) error {
	rt, err := w.runtimeForControl()
	if err != nil {
		return err
	}
	w.mu.Lock()
	if w.serve.cancel != nil {
		w.mu.Unlock()
		return fmt.Errorf("serve is already running")
	}
	serveCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	w.serve.cancel = cancel
	w.serve.done = done
	w.mu.Unlock()
	go func() {
		done <- rt.Serve(serveCtx)
	}()
	return nil
}
