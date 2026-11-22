package temporal

import (
	"context"
	"log/slog"
	"time"

	"go.temporal.io/sdk/worker"

	"github.com/epzero/basamento/core"
	runtimepkg "github.com/epzero/basamento/runtime"
)

// workerStopTimeout is how long Serve waits for in-flight workflow and activity
// tasks to finish after its context is cancelled before the client closes.
const workerStopTimeout = 30 * time.Second

var _ runtimepkg.Runtime = (*Runtime)(nil)

// Serve dials the Temporal server lazily, refuses an unsupported server version
// with core.CodeUnsupported, registers the BasamentoFlow search attribute as a
// Keyword when it is missing, and runs the worker until ctx is cancelled. When
// ctx ends, Serve stops the worker (in-flight tasks get up to workerStopTimeout),
// returns nil, and closes the runtime so later control calls are unavailable.
// If the worker stops itself after a fatal SDK error, Serve returns
// core.CodeUnavailable with a fixed message. Serve may be called at most once;
// a second call returns core.CodeConflict.
func (r *Runtime) Serve(ctx context.Context) error {
	if err := r.beginServe(); err != nil {
		return err
	}
	defer r.shutdown()

	if ctx.Err() != nil {
		return nil
	}

	cl, err := r.clientFor()
	if err = quietIfDone(ctx, err); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}

	if err = quietIfDone(ctx, r.checkServerVersion(ctx, cl)); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}

	if err = quietIfDone(ctx, r.ensureFlowSearchAttribute(ctx, cl)); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return nil
	}

	onFatal, fatalErrCh := newFatalSink()
	w := r.newWorker(cl, r.taskQueue, worker.Options{
		WorkflowPanicPolicy: worker.BlockWorkflow,
		WorkerStopTimeout:   workerStopTimeout,
		OnFatalError:        onFatal,
	})
	if err := r.Register(w); err != nil {
		return err
	}
	if err := w.Start(); err != nil {
		r.logClientFailure("worker_start", err)
		return core.NewError(core.CodeUnavailable, "temporal: cannot start the worker", nil)
	}
	r.logServeStarted()

	select {
	case <-ctx.Done():
		w.Stop()
		r.logServeStopped()
		return nil
	case err := <-fatalErrCh:
		w.Stop()
		r.logClientFailure("worker_fatal", err)
		r.logServeStopped()
		return core.NewError(core.CodeUnavailable,
			"temporal: the worker stopped after a fatal error",
			map[string]any{"op": "worker"})
	}
}

// quietIfDone maps failures from ctx-aware startup steps to nil when ctx is done.
func quietIfDone(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func (r *Runtime) beginServe() error {
	r.serveMu.Lock()
	defer r.serveMu.Unlock()
	if r.served {
		return core.NewError(core.CodeConflict, "temporal: Serve was already called", nil)
	}
	r.clientMu.Lock()
	closed := r.closed
	r.clientMu.Unlock()
	if closed {
		return core.NewError(core.CodeUnavailable, "temporal: the runtime has shut down", nil)
	}
	r.mu.Lock()
	hasHost := r.host != nil
	r.mu.Unlock()
	if !hasHost {
		return core.NewError(core.CodeUnavailable, "temporal: no flow host is registered", nil)
	}
	r.served = true
	return nil
}

func (r *Runtime) logServeStarted() {
	r.runtimeLogger().LogAttrs(context.Background(), slog.LevelInfo, "temporal: serving",
		slog.String("task_queue", r.taskQueue),
		slog.String("namespace", r.clientNamespace()),
	)
}

func (r *Runtime) logServeStopped() {
	r.runtimeLogger().LogAttrs(context.Background(), slog.LevelInfo, "temporal: stopped")
}

// newFatalSink returns an OnFatalError callback and a channel that receives at
// most the first fatal error. The callback runs on an SDK poller goroutine and
// must never block; additional errors are dropped.
func newFatalSink() (func(error), <-chan error) {
	ch := make(chan error, 1)
	return func(err error) {
		select {
		case ch <- err:
		default:
		}
	}, ch
}
