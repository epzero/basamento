package worker

import (
	"context"
	"encoding/json"
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

// Start queues the invocation. It is idempotent per client ref because the
// queue has one row per client ref.
func (i *Invoker) Start(ctx context.Context, r model.Request) (model.Handle, error) {
	tenant, _ := core.TenantFrom(ctx)
	inv, err := i.Store.Enqueue(ctx, Invocation{
		ClientRef:    r.ClientRef,
		Pool:         i.Pool,
		Tenant:       tenant,
		Model:        r.Model,
		Class:        ClassNormal,
		Units:        1,
		Request:      requestPayload(r),
		QueueTimeout: 10 * time.Minute,
	})
	if err != nil {
		return model.Handle{}, err
	}
	return model.Handle{Provider: i.Provider(), ID: inv.ClientRef, ClientRef: inv.ClientRef}, nil
}

func requestPayload(r model.Request) json.RawMessage {
	if len(r.Params) == 0 {
		return json.RawMessage("{}")
	}
	return r.Params
}

// Events delivers the partial output the queue holds and closes when the
// invocation ends.
func (i *Invoker) Events(ctx context.Context, h model.Handle, after int64) (<-chan model.Event, error) {
	if _, err := i.Store.Invocation(ctx, h.ClientRef); err != nil {
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
	return i.Store.Cancel(ctx, h.ClientRef)
}

// Lookup answers from the queue: one row per client ref.
func (i *Invoker) Lookup(ctx context.Context, clientRef string) (*model.Handle, error) {
	if _, err := i.Store.Invocation(ctx, clientRef); err != nil {
		if core.CodeOf(err) == core.CodeNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &model.Handle{Provider: i.Provider(), ID: clientRef, ClientRef: clientRef}, nil
}

var _ model.Invoker = (*Invoker)(nil)
