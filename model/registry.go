package model

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/epzero/basamento/core"
)

// Registry keeps the live invocations of one adapter. It supplies what every
// adapter owes the contract regardless of its vendor: a start that is
// idempotent per client ref, an ordered event log that can be followed from a
// cursor, a result that waits for the end, cancellation and lookup (REQ-MODEL-001,
// ADR-0006). Adapters own the vendor call and publish into a Run.
//
// A Registry is safe for concurrent use. It never forgets an invocation, so a
// retried Start with the same client ref returns the original handle and its
// events and result stay readable.
type Registry struct {
	provider string

	mu    sync.Mutex
	byRef map[string]*Run
}

// NewRegistry returns an empty registry for the named provider.
func NewRegistry(provider string) *Registry {
	return &Registry{provider: provider, byRef: map[string]*Run{}}
}

// Provider is the provider name the registry stamps on every handle.
func (g *Registry) Provider() string { return g.provider }

// Begin returns the run for clientRef, opening one when none exists. The
// boolean reports whether the run already existed, which is how an adapter
// keeps Start idempotent per client ref: it publishes nothing in that case.
// clientRef must not be empty; adapters reject such a request before beginning.
//
// The run's context is detached from ctx's cancellation but keeps its values,
// because an invocation outlives the request that started it (ADR-0005) and
// ends through Cancel, not through the caller's start context.
func (g *Registry) Begin(ctx context.Context, clientRef string) (*Run, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r, ok := g.byRef[clientRef]; ok {
		return r, true
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r := &Run{
		handle:  Handle{Provider: g.provider, ID: clientRef, ClientRef: clientRef},
		ctx:     runCtx,
		cancel:  cancel,
		changed: make(chan struct{}),
		done:    make(chan struct{}),
	}
	g.byRef[clientRef] = r
	return r, false
}

// Events follows the invocation's output with Seq > after. The channel closes
// when the invocation ends and every remaining event has been delivered.
func (g *Registry) Events(ctx context.Context, h Handle, after int64) (<-chan Event, error) {
	r, err := g.run(h)
	if err != nil {
		return nil, err
	}
	return r.eventsAfter(ctx, after), nil
}

// Result waits for the invocation to end and returns its result. A failed or
// cancelled invocation returns that state in the result, not an error; the
// error is reserved for an unknown handle and for the caller's own context.
func (g *Registry) Result(ctx context.Context, h Handle) (Result, error) {
	r, err := g.run(h)
	if err != nil {
		return Result{}, err
	}
	return r.wait(ctx)
}

// Cancel ends the invocation as cancelled and cancels the vendor call's
// context. Cancelling an invocation that already ended is a no-op.
func (g *Registry) Cancel(ctx context.Context, h Handle) error {
	r, err := g.run(h)
	if err != nil {
		return err
	}
	r.Cancel()
	return nil
}

// Lookup resolves a client ref to its invocation. It returns (nil, nil) when
// this adapter has no invocation for the ref.
func (g *Registry) Lookup(ctx context.Context, clientRef string) (*Handle, error) {
	g.mu.Lock()
	r, ok := g.byRef[clientRef]
	g.mu.Unlock()
	if !ok {
		return nil, nil
	}
	h := r.Handle()
	return &h, nil
}

func (g *Registry) run(h Handle) (*Run, error) {
	if h.ClientRef == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "handle has no client ref", nil)
	}
	g.mu.Lock()
	r, ok := g.byRef[h.ClientRef]
	g.mu.Unlock()
	if !ok {
		return nil, core.NewError(core.CodeNotFound,
			fmt.Sprintf("no invocation for client ref %q", h.ClientRef),
			map[string]any{"client_ref": h.ClientRef})
	}
	return r, nil
}

// Run is one live invocation. An adapter publishes its vendor output as
// events and ends it with Succeed, Fail or Cancel.
type Run struct {
	handle Handle
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	events  []Event
	seq     int64
	ended   bool
	result  Result
	changed chan struct{}
	done    chan struct{}
}

// Handle identifies the invocation.
func (r *Run) Handle() Handle { return r.handle }

// Context is the vendor call's context. It is cancelled when the invocation
// is cancelled or ends.
func (r *Run) Context() context.Context { return r.ctx }

// Done is closed when the invocation ends, whether it succeeded, failed or was
// cancelled.
func (r *Run) Done() <-chan struct{} { return r.done }

// Publish appends one event and assigns its sequence number. It returns false
// when the invocation has already ended, in which case the event is dropped.
// Publish is the only writer of r.events; each appended event gets Seq == len
// before append, and after append events[i].Seq == i+1 for every slot.
func (r *Run) Publish(e Event) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return false
	}
	r.seq++
	e.Seq = r.seq
	r.events = append(r.events, e)
	r.notifyLocked()
	return true
}

// Succeed ends the invocation as succeeded.
func (r *Run) Succeed(res Result) bool {
	res.State = StateSucceeded
	return r.Finish(res)
}

// Fail ends the invocation as failed and records the mapped error.
func (r *Run) Fail(err error) bool {
	data, marshalErr := json.Marshal(FailureOf(err))
	if marshalErr != nil {
		data = []byte(`{"code":"internal"}`)
	}
	return r.Finish(Result{State: StateFailed, Error: data})
}

// Finish ends the invocation with res. It returns false when it had already
// ended, so a late vendor callback cannot overwrite a cancellation.
func (r *Run) Finish(res Result) bool {
	r.mu.Lock()
	if r.ended {
		r.mu.Unlock()
		return false
	}
	r.finishLocked(res)
	r.mu.Unlock()
	r.cancel()
	return true
}

// Cancel ends the invocation as cancelled and cancels the vendor call. It
// returns false when the invocation had already ended.
func (r *Run) Cancel() bool {
	r.mu.Lock()
	if r.ended {
		r.mu.Unlock()
		return false
	}
	r.finishLocked(Result{State: StateCancelled})
	r.mu.Unlock()
	r.cancel()
	return true
}

func (r *Run) finishLocked(res Result) {
	if res.State == "" {
		res.State = StateSucceeded
	}
	r.ended = true
	r.result = res
	r.notifyLocked()
	close(r.done)
}

// notifyLocked wakes every waiter. It must be called with r.mu held.
func (r *Run) notifyLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

func (r *Run) eventsAfter(ctx context.Context, after int64) <-chan Event {
	out := make(chan Event)
	go func() {
		defer close(out)
		next := after
		for {
			r.mu.Lock()
			var batch []Event
			start := int(next) + 1
			if start < 0 {
				start = 0
			}
			// Invariant: events[i].Seq == i+1 (Publish is the only writer).
			if start < len(r.events) {
				batch = append([]Event(nil), r.events[start:]...)
			}
			ended, changed := r.ended, r.changed
			r.mu.Unlock()

			for _, e := range batch {
				select {
				case out <- e:
					next = e.Seq
				case <-ctx.Done():
					return
				}
			}
			if len(batch) > 0 {
				continue
			}
			if ended {
				return
			}
			select {
			case <-changed:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

func (r *Run) wait(ctx context.Context) (Result, error) {
	for {
		r.mu.Lock()
		if r.ended {
			res := r.result
			r.mu.Unlock()
			return res, nil
		}
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return Result{}, MapError(ctx.Err())
		}
	}
}
