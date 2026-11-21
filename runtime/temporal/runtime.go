package temporal

import (
	"log/slog"
	"sync"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/platform"
	"github.com/epzero/basamento/runtime"
)

type registeredStep struct {
	fn       runtime.StepFunc
	defaults runtime.StepDefaults
}

// Runtime registers the Temporal flow-host workflow and step activities. It
// does not yet implement runtime.Runtime (WP-L5-02 pull request (c)).
type Runtime struct {
	mu        sync.Mutex
	host      runtime.FlowHost
	steps     map[string]registeredStep
	codec     *Codec
	blobs     platform.Blob
	taskQueue string
	logger    *slog.Logger
	threshold int
}

// New returns a Temporal runtime shell. A nil blob store or codec is refused.
func New(opts Options) (*Runtime, error) {
	if opts.Blobs == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: blob store is required", nil)
	}
	threshold := opts.Threshold
	if threshold <= 0 {
		threshold = DefaultClaimCheckBytes
	}
	if opts.Codec == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: payload codec is required", nil)
	}
	codec := opts.Codec
	queue := opts.TaskQueue
	if queue == "" {
		queue = DefaultTaskQueue
	}
	return &Runtime{
		steps:     map[string]registeredStep{},
		codec:     codec,
		blobs:     opts.Blobs,
		taskQueue: queue,
		threshold: threshold,
		logger:    opts.Logger,
	}, nil
}

// RegisterFlowHost installs the single flow entrypoint (ADR-0011).
func (r *Runtime) RegisterFlowHost(h runtime.FlowHost) error {
	if h == nil {
		return core.NewError(core.CodeInvalidArgument, "temporal: nil flow host", nil)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.host != nil {
		return core.NewError(core.CodeConflict, "temporal: a flow host is already registered", nil)
	}
	r.host = h
	return nil
}

// RegisterStep registers a side-effecting step by name.
func (r *Runtime) RegisterStep(name string, fn runtime.StepFunc, d runtime.StepDefaults) error {
	if name == "" || fn == nil {
		return core.NewError(core.CodeInvalidArgument, "temporal: a step needs a name and a function", map[string]any{"step": name})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.steps[name]; ok {
		return core.NewError(core.CodeConflict, "temporal: step is already registered", map[string]any{"step": name})
	}
	r.steps[name] = registeredStep{fn: fn, defaults: d}
	return nil
}

// Capabilities reports durable Temporal traits for flow code (REQ-RT-039).
func (r *Runtime) Capabilities() runtime.Caps {
	return runtime.Caps{
		Durable:         true,
		SyncGateUpdates: true,
		ContinueAsNew:   true,
		PolyglotQueues:  true,
	}
}

// BuildDataConverter returns the shared context-aware converter, optionally
// recording every encoded history payload for tests.
func (r *Runtime) BuildDataConverter(observe func(Encoded)) (converter.DataConverter, error) {
	return NewDataConverter(r.codec, observe)
}

// RegisterWorkflowsActivities registers the host workflow and step activity on
// a worker or test environment registry.
func (r *Runtime) RegisterWorkflowsActivities(reg worker.Registry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.host == nil {
		return core.NewError(core.CodeUnavailable, "temporal: no flow host is registered", nil)
	}
	reg.RegisterWorkflowWithOptions(r.hostWorkflow, workflow.RegisterOptions{Name: HostWorkflowName})
	reg.RegisterActivityWithOptions(r.runStepActivity, activity.RegisterOptions{Name: StepActivityName})
	return nil
}

// TaskQueue returns the default task queue name.
func (r *Runtime) TaskQueue() string { return r.taskQueue }

// Codec returns the payload codec for direct unit tests.
func (r *Runtime) Codec() *Codec { return r.codec }
