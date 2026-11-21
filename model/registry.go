package model

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
)

// DefaultRetention is how long a registry keeps an ended invocation when no
// retention window is set: one hour after the invocation ends.
const DefaultRetention = time.Hour

// DefaultMaxEnded is how many ended invocations a registry keeps when no ended
// bound is set.
const DefaultMaxEnded = 10_000

// Registry keeps the live invocations of one adapter, scoped to the tenant
// carried by each call's context. It supplies what every adapter owes the
// contract regardless of its vendor: a start that is idempotent per client ref
// within a tenant, an ordered event log that can be followed from a cursor, a
// result that waits for the end, cancellation and lookup (REQ-MODEL-001,
// ADR-0006). Adapters own the vendor call and publish into a Run.
//
// A Registry is safe for concurrent use. It keeps an ended invocation for its
// retention window after its end and no longer, and while it holds more ended
// invocations than its ended bound it evicts them oldest-ended first; it never
// evicts a running invocation (REQ-MODEL-088). An evicted client ref answers
// exactly as one the registry never held: Lookup finds no invocation, Events,
// Result and Cancel answer not_found, and Begin opens a new invocation. The
// sweep runs inline in those methods, on the registry's core.Clock, so what a
// caller observes depends only on end times and clock readings: no timer and
// no goroutine per invocation (design R3).
type Registry struct {
	provider  string
	clock     core.Clock
	retention time.Duration
	maxEnded  int

	mu    sync.Mutex
	byRef map[invocationKey]*Run
	// ended holds ended invocations in end-time order; ended[head:] are the
	// ones still held. An ending invocation is inserted at its time position,
	// after any entry with an equal end time, so two invocations that end at
	// the same instant keep the order in which they ended (design R2).
	ended []endedInvocation
	head  int
}

// endedInvocation is one ended invocation in the registry's retention queue.
type endedInvocation struct {
	key invocationKey
	run *Run
	at  time.Time
}

type invocationKey struct {
	tenant core.TenantID
	ref    string
}

// Option configures a Registry at construction.
type Option func(*registryOptions) error

type registryOptions struct {
	clock     core.Clock
	retention time.Duration
	maxEnded  int
}

// WithClock sets the clock the retention window and end times are read on. It
// is required: a registry never falls back to the system clock, so the gate
// cannot depend on wall time by omission.
func WithClock(clock core.Clock) Option {
	return func(o *registryOptions) error {
		if clock == nil {
			return core.NewError(core.CodeInvalidArgument, "registry: WithClock needs a clock", nil)
		}
		o.clock = clock
		return nil
	}
}

// WithRetention sets both retention bounds. window is how long an ended
// invocation is kept after its end; maxEnded is how many ended invocations are
// kept. A zero window or a zero maxEnded keeps that bound's default
// (DefaultRetention, DefaultMaxEnded); a negative one is invalid_argument.
// There is no value that means unbounded.
func WithRetention(window time.Duration, maxEnded int) Option {
	return func(o *registryOptions) error {
		if window < 0 || maxEnded < 0 {
			return core.NewError(core.CodeInvalidArgument,
				"registry: WithRetention must not be negative", nil)
		}
		if window > 0 {
			o.retention = window
		}
		if maxEnded > 0 {
			o.maxEnded = maxEnded
		}
		return nil
	}
}

// NewRegistry returns an empty registry for the named provider. It requires
// WithClock and fails with invalid_argument without one, naming the option; an
// invalid bound fails the same way.
func NewRegistry(provider string, opts ...Option) (*Registry, error) {
	o := registryOptions{retention: DefaultRetention, maxEnded: DefaultMaxEnded}
	for _, opt := range opts {
		if opt == nil {
			continue
		}
		if err := opt(&o); err != nil {
			return nil, err
		}
	}
	if o.clock == nil {
		return nil, core.NewError(core.CodeInvalidArgument, "registry: WithClock is required", nil)
	}
	return &Registry{
		provider:  provider,
		clock:     o.clock,
		retention: o.retention,
		maxEnded:  o.maxEnded,
		byRef:     map[invocationKey]*Run{},
	}, nil
}

// Provider is the provider name the registry stamps on every handle.
func (g *Registry) Provider() string { return g.provider }

// Begin returns the run for clientRef, opening one when none exists within the
// tenant of ctx. The boolean reports whether the run already existed, which is
// how an adapter keeps Start idempotent per client ref in that tenant: it
// publishes nothing in that case. It fails with invalid_argument when ctx
// carries no tenant. Each run is keyed by (tenant, client ref). clientRef must
// not be empty; adapters reject such a request before beginning.
//
// The run's context is detached from ctx's cancellation but keeps its values,
// because an invocation outlives the request that started it (ADR-0005) and
// ends through Cancel, not through the caller's start context.
func (g *Registry) Begin(ctx context.Context, clientRef string) (*Run, bool, error) {
	tid, ok := core.TenantFrom(ctx)
	if !ok {
		return nil, false, core.NewError(core.CodeInvalidArgument, "tenant required", nil)
	}
	if clientRef == "" {
		return nil, false, core.NewError(core.CodeInvalidArgument, "client ref required", nil)
	}
	key := invocationKey{tenant: tid, ref: clientRef}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sweepLocked()
	if r, ok := g.byRef[key]; ok {
		return r, true, nil
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r := &Run{
		handle:  Handle{Provider: g.provider, ID: clientRef, ClientRef: clientRef},
		reg:     g,
		key:     key,
		ctx:     runCtx,
		cancel:  cancel,
		changed: make(chan struct{}),
		done:    make(chan struct{}),
	}
	g.byRef[key] = r
	return r, false, nil
}

// Events follows the invocation's output with Seq > after. It requires the
// tenant of ctx; a handle that belongs to another tenant, or to nobody, is
// not_found like an unknown handle. The channel closes when the invocation
// ends and every remaining event has been delivered.
func (g *Registry) Events(ctx context.Context, h Handle, after int64) (<-chan Event, error) {
	r, err := g.run(ctx, h)
	if err != nil {
		return nil, err
	}
	return r.eventsAfter(ctx, after), nil
}

// Result waits for the invocation to end and returns its result. It requires
// the tenant of ctx; a handle that belongs to another tenant, or to nobody, is
// not_found like an unknown handle. A failed or cancelled invocation returns
// that state in the result, not an error; the error is reserved for an unknown
// handle and for the caller's own context.
func (g *Registry) Result(ctx context.Context, h Handle) (Result, error) {
	r, err := g.run(ctx, h)
	if err != nil {
		return Result{}, err
	}
	return r.wait(ctx)
}

// Cancel ends the invocation as cancelled and cancels the vendor call's
// context. It requires the tenant of ctx; a handle that belongs to another
// tenant, or to nobody, is not_found like an unknown handle. Cancelling an
// invocation that already ended is a no-op.
func (g *Registry) Cancel(ctx context.Context, h Handle) error {
	r, err := g.run(ctx, h)
	if err != nil {
		return err
	}
	r.Cancel()
	return nil
}

// Lookup resolves a client ref to its invocation within the tenant of ctx. It
// requires that tenant; without one it fails with invalid_argument. A client
// ref that belongs to another tenant, or to nobody, returns (nil, nil), the
// same as an unknown ref in this tenant. A registry only resolves refs it owns.
func (g *Registry) Lookup(ctx context.Context, clientRef string) (*Handle, error) {
	tid, ok := core.TenantFrom(ctx)
	if !ok {
		return nil, core.NewError(core.CodeInvalidArgument, "tenant required", nil)
	}
	if clientRef == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "client ref required", nil)
	}
	g.mu.Lock()
	g.sweepLocked()
	r, ok := g.byRef[invocationKey{tenant: tid, ref: clientRef}]
	g.mu.Unlock()
	if !ok {
		return nil, nil
	}
	h := r.Handle()
	return &h, nil
}

func (g *Registry) run(ctx context.Context, h Handle) (*Run, error) {
	if h.ClientRef == "" {
		return nil, core.NewError(core.CodeInvalidArgument, "handle has no client ref", nil)
	}
	if h.Provider != "" && h.Provider != g.provider {
		return nil, core.NewError(core.CodeNotFound,
			fmt.Sprintf("no invocation for client ref %q", h.ClientRef),
			map[string]any{"client_ref": h.ClientRef})
	}
	tid, ok := core.TenantFrom(ctx)
	if !ok {
		return nil, core.NewError(core.CodeInvalidArgument, "tenant required", nil)
	}
	g.mu.Lock()
	g.sweepLocked()
	r, ok := g.byRef[invocationKey{tenant: tid, ref: h.ClientRef}]
	g.mu.Unlock()
	if !ok {
		return nil, core.NewError(core.CodeNotFound,
			fmt.Sprintf("no invocation for client ref %q", h.ClientRef),
			map[string]any{"client_ref": h.ClientRef})
	}
	return r, nil
}

// sweepLocked evicts the ended invocations that left their retention window,
// then the oldest ended ones past the ended bound. The queue is in end-time
// order, so both rules pop its head; two invocations that ended at the same
// instant leave in the order they ended. It must be called with g.mu held and
// never looks at a run's own lock, so an invocation ending under r.mu can
// report itself to g.mu without a lock inversion (design R3).
func (g *Registry) sweepLocked() {
	now := g.clock.Now()
	for g.head < len(g.ended) && !g.ended[g.head].at.Add(g.retention).After(now) {
		g.forgetLocked(g.head)
		g.head++
	}
	for len(g.ended)-g.head > g.maxEnded {
		g.forgetLocked(g.head)
		g.head++
	}
	// Compact once the consumed prefix is half the queue. A burst leaves a
	// backing array sized for its peak; when the live entries are far below
	// that capacity, a right-sized copy replaces the slice so the peak array
	// can be collected. Otherwise the copy stays in place, and a steady state
	// then keeps a capacity of at most twice the live entries.
	if g.head > 0 && g.head*2 >= len(g.ended) {
		live := len(g.ended) - g.head
		if cap(g.ended) > 2*live {
			kept := make([]endedInvocation, live)
			copy(kept, g.ended[g.head:])
			g.ended = kept
		} else {
			n := copy(g.ended, g.ended[g.head:])
			clear(g.ended[n:])
			g.ended = g.ended[:n]
		}
		g.head = 0
	}
}

// forgetLocked drops the ended invocation at the queue's head: its client ref
// no longer resolves and the slot stops holding its event log, so the run is
// free to be collected even before the next compaction.
func (g *Registry) forgetLocked(i int) {
	e := g.ended[i]
	g.ended[i] = endedInvocation{}
	if cur, ok := g.byRef[e.key]; ok && cur == e.run {
		delete(g.byRef, e.key)
	}
}

// runEnded inserts a run that just ended into the retention queue in end-time
// order, stamped with the registry's clock. It inserts after every entry with
// an equal end time, so a tie keeps the order in which its invocations ended
// even when the clock steps back between them (design R2). The sweep that
// follows evicts on the next registry method call, never in this one, so it
// only reads the queue.
func (g *Registry) runEnded(key invocationKey, run *Run) {
	g.mu.Lock()
	at := g.clock.Now()
	live := g.ended[g.head:]
	i := len(live)
	if n := len(live); n > 0 && live[n-1].at.After(at) {
		// First entry that ends strictly after at; an equal end stays before
		// the insertion point, so it keeps its end order.
		i = sort.Search(n, func(j int) bool { return live[j].at.After(at) })
	}
	g.ended = append(g.ended, endedInvocation{})
	pos := g.head + i
	copy(g.ended[pos+1:], g.ended[pos:])
	g.ended[pos] = endedInvocation{key: key, run: run, at: at}
	g.mu.Unlock()
}

// Run is one live invocation. An adapter publishes its vendor output as
// events and ends it with Succeed, Fail or Cancel.
type Run struct {
	handle Handle
	ctx    context.Context
	cancel context.CancelFunc
	reg    *Registry
	key    invocationKey

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

// Fail ends the invocation as failed. It maps err with MapError then records
// FailureOf(err) so Result.Error never carries raw vendor text; the original
// stays on MapError's Cause for in-process callers.
func (r *Run) Fail(err error) bool {
	err = MapError(err)
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
	if r.reg != nil {
		r.reg.runEnded(r.key, r)
	}
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
			n := int64(len(r.events))
			idx := next
			if idx < 0 {
				idx = 0
			}
			if idx >= n {
				idx = n
			}
			// Invariant: events[i].Seq == i+1 (Publish is the only writer).
			if idx < n {
				batch = append([]Event(nil), r.events[int(idx):]...)
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
