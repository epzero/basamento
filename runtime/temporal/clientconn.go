package temporal

import (
	"log/slog"

	"go.temporal.io/sdk/client"
	sdklog "go.temporal.io/sdk/log"

	"github.com/epzero/basamento/core"
)

func workflowLogger(l *slog.Logger) sdklog.Logger {
	return sdklog.NewStructuredLogger(l)
}

func (r *Runtime) clientNamespace() string {
	if r.clientOpts.Namespace == "" {
		return client.DefaultNamespace
	}
	return r.clientOpts.Namespace
}

// clientFor returns the shared Temporal client, dialing on first use.
func (r *Runtime) clientFor() (client.Client, error) {
	r.clientMu.Lock()
	defer r.clientMu.Unlock()
	if r.closed {
		return nil, core.NewError(core.CodeUnavailable, "temporal: the runtime has shut down", nil)
	}
	if r.client != nil {
		return r.client, nil
	}
	cl, err := r.dial(r.clientOpts)
	if err != nil {
		r.logClientFailure("dial", err)
		return nil, core.NewError(core.CodeUnavailable, "temporal: cannot reach the Temporal server", nil)
	}
	r.client = cl
	return cl, nil
}

// Close releases the lazily dialled Temporal client when one exists and marks the
// runtime shut down so later control calls and Serve return unavailable. It is not
// part of runtime.Runtime. The method implements io.Closer; the returned error is
// always nil. Close waits for a dial in progress (the SDK bounds the dial; this
// runtime adds no bound) and does not wait for in-flight control calls (they fail
// with cancelled when the connection closes). Close does not stop a running Serve
// (the host cancels Serve's context; Serve's defer performs the same shutdown).
// It is idempotent and safe to call more than once and concurrently with control
// calls.
func (r *Runtime) Close() error {
	r.shutdown()
	return nil
}

// shutdown marks the runtime closed and closes the client if one was opened.
func (r *Runtime) shutdown() {
	r.clientMu.Lock()
	defer r.clientMu.Unlock()
	r.closed = true
	if r.client != nil {
		r.client.Close()
		r.client = nil
	}
}
