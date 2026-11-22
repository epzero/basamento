package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
)

// pollInterval is how often the queue-backed invoker looks for a terminal
// state. The queue is the source of truth; the interval only bounds latency.
const pollInterval = 100 * time.Millisecond

// Invoker is the platform side of a pool as a model.Invoker: starting queues
// the invocation, Lookup is always answered from the queue, and the result is
// the worker's report or the queue's timeout (ADR-0012 §7). To a flow it is
// one more provider.
type Invoker struct {
	Store *Store
	// Pool is the pool the invocation is queued for.
	Pool string
	// Caps are the capabilities of the models in the pool, for routing.
	Caps []model.Capability
}

// Provider names the provider for router registrations.
func (i *Invoker) Provider() string { return "worker:" + i.Pool }

// Describe lists the models the pool serves, with locality worker.
func (i *Invoker) Describe(ctx context.Context) ([]model.Descriptor, error) {
	descs, err := i.Store.Descriptors(ctx, i.Pool)
	if err != nil {
		return nil, err
	}
	out := make([]model.Descriptor, 0, len(descs))
	for _, d := range descs {
		desc := d.Descriptor
		desc.Locality = model.LocalityWorker
		out = append(out, desc)
	}
	return out, nil
}

// StartOptions are a run's start options for one worker invocation: the
// priority class the run chose (ADR-0012 §5) and its queue timeout. The zero
// value means normal class and the platform's default queue timeout.
type StartOptions struct {
	Class        Class
	QueueTimeout time.Duration
	MaxRuntime   time.Duration
	ReservedMpts int64
	TraceParent  string
}

// DefaultQueueTimeout is how long an invocation may wait for a worker when
// the run did not choose a queue timeout.
const DefaultQueueTimeout = 10 * time.Minute

type startOptionsKey struct{}

// WithStartOptions returns a context carrying the run's start options.
func WithStartOptions(ctx context.Context, opts StartOptions) context.Context {
	return context.WithValue(ctx, startOptionsKey{}, opts)
}

// StartOptionsFrom returns the run's start options, with defaults filled in.
func StartOptionsFrom(ctx context.Context) StartOptions {
	opts, _ := ctx.Value(startOptionsKey{}).(StartOptions)
	if opts.Class == "" {
		opts.Class = ClassNormal
	}
	if opts.QueueTimeout == 0 {
		opts.QueueTimeout = DefaultQueueTimeout
	}
	return opts
}

// Start queues the invocation. It is idempotent per client ref because the
// queue has one row per client ref. The units are the pool descriptor's
// (ADR-0012 §6); the class and queue timeout are the run's start options.
func (i *Invoker) Start(ctx context.Context, r model.Request) (model.Handle, error) {
	tenant, _ := core.TenantFrom(ctx)
	desc, err := i.Store.descriptorFor(ctx, i.Pool, r.Model)
	if err != nil {
		return model.Handle{}, err
	}
	if !descriptorSupports(desc.Descriptor, r.Op) {
		return model.Handle{}, core.NewError(core.CodeUnsupported,
			fmt.Sprintf("model %q does not support %s", r.Model, r.Op),
			map[string]any{"model": r.Model, "op": string(r.Op)})
	}
	units := desc.Units
	if units <= 0 {
		units = 1
	}
	payload, err := encodeRequest(r)
	if err != nil {
		return model.Handle{}, err
	}
	opts := StartOptionsFrom(ctx)
	inv, err := i.Store.Enqueue(ctx, Invocation{
		ClientRef:    r.ClientRef,
		Pool:         i.Pool,
		Tenant:       tenant,
		Model:        r.Model,
		Class:        opts.Class,
		Units:        units,
		Request:      payload,
		QueueTimeout: opts.QueueTimeout,
		MaxRuntime:   opts.MaxRuntime,
		ReservedMpts: opts.ReservedMpts,
		TraceParent:  opts.TraceParent,
	})
	if err != nil {
		return model.Handle{}, err
	}
	// A client ref is scoped to the tenant that started it. A shared pool's
	// queue has one row per ref, so a second tenant reusing a ref would
	// otherwise get the first tenant's row and a handle to it.
	if got, ok := core.TenantFrom(ctx); ok && inv.Tenant != got {
		return model.Handle{}, notFoundInvocation(inv.ClientRef)
	}
	return model.Handle{Provider: i.Provider(), ID: inv.ClientRef, ClientRef: inv.ClientRef}, nil
}

// load reads an invocation a handle names and checks that it belongs to the
// caller's tenant and to this provider. A mismatch is not_found, never a
// permission error: the caller must not learn that another tenant's ref
// exists (model.Invoker: handles are valid only for their tenant).
func (i *Invoker) load(ctx context.Context, h model.Handle) (Invocation, error) {
	if h.Provider != "" && h.Provider != i.Provider() {
		return Invocation{}, notFoundInvocation(h.ClientRef)
	}
	inv, err := i.Store.Invocation(ctx, h.ClientRef)
	if err != nil {
		return Invocation{}, err
	}
	tenant, ok := core.TenantFrom(ctx)
	if !ok || inv.Tenant != tenant {
		return Invocation{}, notFoundInvocation(h.ClientRef)
	}
	return inv, nil
}

func notFoundInvocation(clientRef string) error {
	return core.NewError(core.CodeNotFound, "invocation does not exist",
		map[string]any{"client_ref": clientRef})
}

// encodeRequest stores the whole request, not only its parameters: the worker
// rebuilds it faithfully before it calls its provider (ADR-0012 §7).
func encodeRequest(r model.Request) (json.RawMessage, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return nil, core.NewError(core.CodeInvalidArgument,
			"worker: invocation request is not encodable", map[string]any{"cause": err.Error()})
	}
	return raw, nil
}

// decodeRequest rebuilds the request a worker queued. A request recorded
// before the whole request was carried held only parameters; such a payload
// is passed through as parameters. A full request always names its model and
// its client ref, so a bare params object with a "model" key is not mistaken
// for one.
func decodeRequest(inv Invocation) (model.Request, error) {
	req := model.Request{}
	if len(inv.Request) > 0 && string(inv.Request) != "{}" {
		var probe struct {
			Model     string `json:"model"`
			ClientRef string `json:"client_ref"`
		}
		if err := json.Unmarshal(inv.Request, &probe); err != nil {
			return model.Request{}, core.NewError(core.CodeInvalidArgument,
				"worker: invocation request is not a model.Request", map[string]any{"cause": err.Error()})
		}
		if probe.Model == "" || probe.ClientRef == "" {
			req.Params = inv.Request
		} else if err := json.Unmarshal(inv.Request, &req); err != nil {
			return model.Request{}, core.NewError(core.CodeInvalidArgument,
				"worker: invocation request is not a model.Request", map[string]any{"cause": err.Error()})
		}
	}
	if req.Model == "" {
		req.Model = inv.Model
	}
	if req.ClientRef == "" {
		req.ClientRef = inv.ClientRef
	}
	return req, nil
}

// Events delivers the partial output the queue holds and closes when the
// invocation ends.
func (i *Invoker) Events(ctx context.Context, h model.Handle, after int64) (<-chan model.Event, error) {
	if _, err := i.load(ctx, h); err != nil {
		return nil, err
	}
	out := make(chan model.Event, 16)
	go func() {
		defer close(out)
		last := after
		for {
			events, err := i.Store.EventsAfter(ctx, h.ClientRef, last)
			if err != nil {
				return
			}
			for _, e := range events {
				if e.Seq <= last {
					continue
				}
				last = e.Seq
				select {
				case out <- model.Event{Seq: e.Seq, Kind: model.EventKind(e.Kind), Text: e.Text, Audio: e.Audio,
					Progress: e.Progress, Attempt: e.Attempt, Data: e.Data}:
				case <-ctx.Done():
					return
				}
			}
			inv, err := i.Store.Invocation(ctx, h.ClientRef)
			if err != nil || isTerminal(inv.State) {
				return
			}
			select {
			case <-time.After(pollInterval):
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// Result waits for the queue's terminal state and maps it to the contract.
func (i *Invoker) Result(ctx context.Context, h model.Handle) (model.Result, error) {
	if _, err := i.load(ctx, h); err != nil {
		return model.Result{}, err
	}
	for {
		inv, err := i.Store.Invocation(ctx, h.ClientRef)
		if err != nil {
			return model.Result{}, err
		}
		if isTerminal(inv.State) {
			return resultOf(inv)
		}
		select {
		case <-time.After(pollInterval):
		case <-ctx.Done():
			return model.Result{}, model.MapError(ctx.Err())
		}
	}
}

func resultOf(inv Invocation) (model.Result, error) {
	res := model.Result{CostMpts: inv.ChargedMpts}
	if len(inv.Result) > 0 && string(inv.Result) != "{}" {
		res.Message = inv.Result
	}
	switch inv.State {
	case stateSucceeded, stateRemote:
		res.State = model.StateSucceeded
		return res, nil
	case stateCancelled:
		res.State = model.StateCancelled
		return res, nil
	case stateFailed:
		res.State = model.StateFailed
		code := inv.Code
		if code == "" {
			code = core.CodeUnavailable
		}
		return res, core.NewError(code, "the invocation could not be completed",
			map[string]any{"client_ref": inv.ClientRef})
	}
	return res, nil
}

// Cancel removes a waiting invocation or asks the worker to cancel a lease.
func (i *Invoker) Cancel(ctx context.Context, h model.Handle) error {
	if _, err := i.load(ctx, h); err != nil {
		return err
	}
	return i.Store.Cancel(ctx, h.ClientRef)
}

// Lookup answers from the queue: one row per client ref.
func (i *Invoker) Lookup(ctx context.Context, clientRef string) (*model.Handle, error) {
	inv, err := i.Store.Invocation(ctx, clientRef)
	if err != nil {
		if core.CodeOf(err) == core.CodeNotFound {
			return nil, nil
		}
		return nil, err
	}
	tenant, ok := core.TenantFrom(ctx)
	if !ok || inv.Tenant != tenant {
		return nil, nil
	}
	return &model.Handle{Provider: i.Provider(), ID: clientRef, ClientRef: clientRef}, nil
}

var _ model.Invoker = (*Invoker)(nil)
