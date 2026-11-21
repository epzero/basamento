package temporal

import (
	"log/slog"
	"sync"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
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
	converter converter.DataConverter
	taskQueue string
	logger    *slog.Logger
}

// New returns a Temporal runtime shell. The shared data converter performs blob
// I/O inside workflow code; New wraps it with workflow.DataConverterWithoutDeadlockDetection
// so SDK deadlock detection does not count that I/O (slow stores delay the task,
// not the detector).
func New(opts Options) (*Runtime, error) {
	if opts.Blobs == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: blob store is required", nil)
	}
	if opts.Payloads == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "temporal: payload codec is required", nil)
	}
	threshold := opts.ClaimCheckBytes
	if threshold <= 0 {
		threshold = DefaultClaimCheckBytes
	}
	codec, err := newPayloadCodec(opts.Payloads, opts.Blobs, threshold)
	if err != nil {
		return nil, err
	}
	dc, err := newDataConverter(codec)
	if err != nil {
		return nil, err
	}
	// Blob I/O during workflow encode/decode must not count against the SDK deadlock detector.
	dc = workflow.DataConverterWithoutDeadlockDetection(dc)
	queue := opts.TaskQueue
	if queue == "" {
		queue = DefaultTaskQueue
	}
	return &Runtime{
		steps:     map[string]registeredStep{},
		converter: dc,
		taskQueue: queue,
		logger:    opts.Logger,
	}, nil
}

func (r *Runtime) stepFor(name string) (registeredStep, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.steps[name]
	return reg, ok
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

// DataConverter returns the shared context-aware converter for clients and workers.
func (r *Runtime) DataConverter() converter.DataConverter {
	return r.converter
}

// Register installs the host workflow and step activity on a worker registry.
func (r *Runtime) Register(reg worker.Registry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.host == nil {
		return core.NewError(core.CodeUnavailable, "temporal: no flow host is registered", nil)
	}
	reg.RegisterWorkflowWithOptions(r.hostWorkflow, workflow.RegisterOptions{Name: HostWorkflowName})
	reg.RegisterActivityWithOptions(r.runStepActivity, activity.RegisterOptions{Name: stepActivityName})
	return nil
}

// defaultStartToClose is the activity start-to-close timeout when none is set.
const defaultStartToClose = 24 * time.Hour
