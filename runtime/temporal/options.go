package temporal

import (
	"log/slog"

	"go.temporal.io/sdk/client"

	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/platform/secrets"
)

// DefaultTaskQueue is the workflow and default activity task queue name.
const DefaultTaskQueue = "basamento-flow"

// HostWorkflowName is the registered name of the flow-host workflow type.
const HostWorkflowName = "basamento-host"

// DefaultClaimCheckBytes is the encrypted-payload size above which payloads
// are stored in blob storage (REQ-RT-028).
const DefaultClaimCheckBytes = 5 * 1024

const stepActivityName = "basamento-step"

// Options configures a Runtime at the composition root.
type Options struct {
	// Payloads is the tenant encryption codec (WP-L1-04).
	Payloads *secrets.Codec
	// Blobs stores claim-checked ciphertext (WP-L1-02).
	Blobs platform.Blob
	// ClaimCheckBytes is the encrypted size threshold; 0 means DefaultClaimCheckBytes.
	ClaimCheckBytes int
	// TaskQueue names the workflow and default activity queue; empty means DefaultTaskQueue.
	TaskQueue string
	// Logger records panics and non-core step or flow failures.
	Logger *slog.Logger
	// Outbox persists durable progress events (required).
	Outbox Outbox
	// CheckpointEventThreshold selects when Checkpoint continues as new; 0 means
	// DefaultCheckpointEventThreshold (2000); negative disables continue-as-new.
	CheckpointEventThreshold int
	// Client carries host, namespace, TLS and credentials for the Temporal service.
	// DataConverter and ContextPropagators are owned by the runtime and must not be set.
	Client client.Options
}
