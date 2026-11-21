package temporal

import (
	"context"

	"github.com/epzero/basamento/runtime"
)

// Outbox persists durable progress events for a run. The composition root
// supplies an implementation; WP-L6-02 connects it to stream/eventlog.
// Production code in this package does not include an in-memory Outbox.
//
// Append stores ev at the run's outbox position seq (1-based). When the same
// seq is appended again (activity retry or workflow replay), the sink must not
// store a second copy.
type Outbox interface {
	Append(ctx context.Context, run runtime.RunRef, seq uint64, ev runtime.ProgressEvent) error
}
