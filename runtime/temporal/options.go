package temporal

import (
	"log/slog"
	"time"

	"github.com/epzero/basamento/platform"
)

// DefaultTaskQueue is the workflow and default activity task queue name.
const DefaultTaskQueue = "basamento-flow"

// HostWorkflowName is the registered name of the flow-host workflow type.
const HostWorkflowName = "basamento-host"

// StepActivityName is the registered name of the step activity.
const StepActivityName = "basamento-step"

// DefaultClaimCheckBytes is the encrypted-payload size above which payloads
// are stored in blob storage (REQ-RT-028).
const DefaultClaimCheckBytes = 5 * 1024

// defaultStartToClose is the activity start-to-close timeout when none is set.
const defaultStartToClose = 24 * time.Hour

// Options configures a Runtime. The blob store and payload codec come from the
// composition root; claim-check threshold and task queue tune history size.
type Options struct {
	Blobs     platform.Blob
	Codec     *Codec
	Threshold int    // encrypted bytes; 0 means DefaultClaimCheckBytes
	TaskQueue string // empty means DefaultTaskQueue
	Logger    *slog.Logger
}
