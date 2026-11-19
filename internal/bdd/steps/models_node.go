package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/catalog"
	"github.com/epzero/basamento/model/node"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/runtime/local"
)

// nodeWorld drives the model node handler through a real local runtime: the
// scenarios speak of a run that invokes a model, so the handler runs as the
// model node of a one-node flow.
type nodeWorld struct {
	provider *nodeProvider
	catalog  *catalog.Catalog
	budget   *node.Memory
	handler  *node.Handler
	rt       *local.Runtime
	ref      runtime.RunRef
	running  bool

	doneCh chan struct{}
	gateCh chan string

	mu        sync.Mutex
	atGate    string
	runErr    error
	out       node.Output
	heldStart int64
	heldSet   bool
}

func newNodeWorld() *nodeWorld {
	return &nodeWorld{doneCh: make(chan struct{}), gateCh: make(chan string, 4)}
}

// nodeProvider simulates a video vendor at the Invoker contract. It records
// every attempt so a scenario can read back how many starts the provider saw
// and whether they carried one client ref.
type nodeProvider struct {
	mu   sync.Mutex
	desc model.Descriptor

	rejectFirst  bool
	dropResponse bool
	needConfirm  bool
	lookup       bool
	forget       bool

	starts   int
	refs     []string
	runs     map[string]*providerRun
	accepted int
	lookups  int
	found    bool
	cancels  int

	acceptedCh chan struct{}
	followedCh chan struct{}
	lookupCh   chan struct{}
	cancelCh   chan struct{}
	world      *nodeWorld
}

type providerRun struct {
	handle model.Handle
	events []model.Event
	done   chan struct{}
	result model.Result
	closed bool
}

func newNodeProvider(w *nodeWorld, id string, op model.Capability) *nodeProvider {
	return &nodeProvider{
		desc: model.Descriptor{
			ID:       id,
			Provider: "sim",
			Caps:     []model.Capability{op},
			Params:   json.RawMessage(`{"type":"object"}`),
			Protocol: model.ProtocolPoll,
			Locality: model.LocalityCloud,
		},
		lookup:     true,
		runs:       map[string]*providerRun{},
		acceptedCh: make(chan struct{}, 16),
		followedCh: make(chan struct{}, 16),
		lookupCh:   make(chan struct{}, 4),
		cancelCh:   make(chan struct{}, 4),
		world:      w,
	}
}

func (p *nodeProvider) Describe(context.Context) ([]model.Descriptor, error) {
	return []model.Descriptor{p.desc}, nil
}

func (p *nodeProvider) Start(_ context.Context, req model.Request) (model.Handle, error) {
	p.mu.Lock()
	p.starts++
	p.refs = append(p.refs, req.ClientRef)
	if p.rejectFirst && p.starts == 1 {
		p.mu.Unlock()
		return model.Handle{}, rejected()
	}
	if run, ok := p.runs[req.ClientRef]; ok {
		handle := run.handle
		p.mu.Unlock()
		return handle, nil
	}
	run := &providerRun{
		handle: model.Handle{Provider: "sim", ID: req.ClientRef, ClientRef: req.ClientRef},
		events: []model.Event{{Seq: 1, Kind: model.EventProgress, Progress: 0.1}},
		done:   make(chan struct{}),
	}
	p.runs[req.ClientRef] = run
	p.accepted++
	drop := p.dropResponse
	p.recordHeld()
	p.mu.Unlock()
	select {
	case p.acceptedCh <- struct{}{}:
	default:
	}
	if drop {
		return model.Handle{}, transient()
	}
	return run.handle, nil
}

// recordHeld remembers what the run held when the provider accepted the
// invocation, so a scenario can assert the reservation came first.
func (p *nodeProvider) recordHeld() {
	w := p.world
	if w == nil || w.budget == nil {
		return
	}
	w.mu.Lock()
	w.heldStart = w.budget.HeldTotal()
	w.heldSet = true
	w.mu.Unlock()
}

func (p *nodeProvider) Events(ctx context.Context, h model.Handle, after int64) (<-chan model.Event, error) {
	p.mu.Lock()
	run := p.runs[h.ClientRef]
	p.mu.Unlock()
	if run == nil {
		return nil, core.NewError(core.CodeNotFound, "no invocation", map[string]any{"client_ref": h.ClientRef})
	}
	// The run's handler is now following the invocation, so a cancel reaches
	// the step that delivers it to the provider instead of ending the run
	// first (MODEL-009-01, EP0-198).
	select {
	case p.followedCh <- struct{}{}:
	default:
	}
	out := make(chan model.Event, len(run.events))
	for _, event := range run.events {
		if event.Seq > after {
			out <- event
		}
	}
	go func() {
		defer close(out)
		select {
		case <-run.done:
		case <-ctx.Done():
		}
	}()
	return out, nil
}

func (p *nodeProvider) Result(ctx context.Context, h model.Handle) (model.Result, error) {
	p.mu.Lock()
	run := p.runs[h.ClientRef]
	p.mu.Unlock()
	if run == nil {
		return model.Result{}, core.NewError(core.CodeNotFound, "no invocation", map[string]any{"client_ref": h.ClientRef})
	}
	select {
	case <-run.done:
		p.mu.Lock()
		result := run.result
		p.mu.Unlock()
		return result, nil
	case <-ctx.Done():
		return model.Result{}, model.MapError(ctx.Err())
	}
}

func (p *nodeProvider) Cancel(_ context.Context, h model.Handle) error {
	p.mu.Lock()
	p.cancels++
	run := p.runs[h.ClientRef]
	confirm := p.needConfirm
	if run != nil && !confirm && !run.closed {
		run.result = model.Result{State: model.StateCancelled}
		run.closed = true
		close(run.done)
	}
	p.mu.Unlock()
	select {
	case p.cancelCh <- struct{}{}:
	default:
	}
	return nil
}

// confirm completes a cancelled invocation with the cost the provider
// reports.
func (p *nodeProvider) confirm(h model.Handle, cost int64) {
	p.mu.Lock()
	run := p.runs[h.ClientRef]
	if run != nil && !run.closed {
		run.result = model.Result{State: model.StateCancelled, CostMpts: cost}
		run.closed = true
		close(run.done)
	}
	p.mu.Unlock()
}

func (p *nodeProvider) Lookup(_ context.Context, clientRef string) (*model.Handle, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lookups++
	if !p.lookup {
		return nil, core.NewError(core.CodeUnsupported, "the provider cannot look an invocation up", nil)
	}
	if p.forget {
		// The invocation's retention window passed: the registry swept it, so
		// the lookup finds nothing, exactly like a ref it never held.
		delete(p.runs, clientRef)
		return nil, nil
	}
	run, ok := p.runs[clientRef]
	if !ok {
		return nil, nil
	}
	p.found = true
	handle := run.handle
	select {
	case p.lookupCh <- struct{}{}:
	default:
	}
	return &handle, nil
}

func transient() error {
	return &core.Error{Code: core.CodeTimeout, Message: "the start response was lost", Retryable: true}
}

// rejected is a retryable start error the provider returned before accepting
// the invocation: the node may retry with the same client ref.
func rejected() error {
	return &core.Error{Code: core.CodeUnavailable, Message: "the provider rejected the start", Retryable: true}
}

// nodeDC records the gate a handler waits at, then delegates to the runtime.
type nodeDC struct {
	runtime.DurableContext
	w *nodeWorld
}

func (d nodeDC) Await(spec runtime.GateSpec) runtime.Future {
	d.w.mu.Lock()
	d.w.atGate = spec.Name
	d.w.mu.Unlock()
	select {
	case d.w.gateCh <- spec.Name:
	default:
	}
	return d.DurableContext.Await(spec)
}

// singleCatalog serves the one flow a node scenario runs.
type singleCatalog struct{ def *flow.IR }

func (c singleCatalog) Load(context.Context, runtime.FlowRef) (*flow.IR, error) {
	if c.def == nil {
		return nil, core.NewError(core.CodeNotFound, "no flow", nil)
	}
	return c.def, nil
}

func (w *nodeWorld) ensureCatalog(ctx context.Context) *catalog.Catalog {
	if w.catalog != nil {
		return w.catalog
	}
	c, err := catalog.New(scenarioClock{w: world(ctx)})
	if err != nil {
		panic(err)
	}
	w.catalog = c
	return c
}

func (w *nodeWorld) ensureProvider(ctx context.Context, op model.Capability) *nodeProvider {
	if w.provider != nil {
		return w.provider
	}
	w.provider = newNodeProvider(w, "sim-video", op)
	if err := w.ensureCatalog(ctx).AddDescriptor(w.provider.desc); err != nil {
		panic(err)
	}
	return w.provider
}

// update changes the scenario's simulated model and republishes its
// descriptor.
func (w *nodeWorld) update(ctx context.Context, change func(*model.Descriptor)) {
	p := w.ensureProvider(ctx, model.CapVideoGenerate)
	p.desc = updated(p.desc, change)
	if err := w.ensureCatalog(ctx).AddDescriptor(p.desc); err != nil {
		panic(err)
	}
}

func updated(d model.Descriptor, change func(*model.Descriptor)) model.Descriptor {
	change(&d)
	return d
}

func (w *nodeWorld) startRun(ctx context.Context, op model.Capability, input string) error {
	if w.running {
		return fmt.Errorf("a run is already in flight")
	}
	if strings.TrimSpace(input) == "" {
		input = "{}"
	}
	p := w.ensureProvider(ctx, op)
	cat := w.ensureCatalog(ctx)
	rt, err := local.New(local.Options{Clock: world(ctx).Core.clock, IDs: &coretest.SeqIds{}})
	if err != nil {
		return err
	}
	h, err := node.New(node.Deps{Invoker: p, Catalog: cat, Steps: rt})
	if err != nil {
		return err
	}
	w.handler = h
	w.rt = rt

	def := &flow.IR{
		Name:    "invoke",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"generate": {
				Kind: "model",
				Spec: json.RawMessage(fmt.Sprintf(`{"model":%q,"op":%q}`, p.desc.ID, op)),
			},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "generate"}},
	}
	flowHost := host.New(singleCatalog{def: def})
	flowHost.RegisterHandler("model", w.handle)
	w.doneCh = make(chan struct{})
	w.gateCh = make(chan string, 4)
	w.running = true
	run := func(dc runtime.DurableContext, ref runtime.FlowRef, in json.RawMessage) (json.RawMessage, error) {
		out, err := flowHost.Run(dc, ref, in)
		close(w.doneCh)
		return out, err
	}
	if err := rt.RegisterFlowHost(run); err != nil {
		return err
	}

	runCtx := core.WithTenant(context.Background(), "acme")
	if w.budget != nil {
		runCtx = node.WithBudget(runCtx, w.budget)
	}
	ref, err := rt.Start(runCtx, runtime.StartRequest{
		Flow:  runtime.FlowRef{Name: def.Name},
		Input: json.RawMessage(input),
	})
	if err != nil {
		return err
	}
	w.ref = ref
	return nil
}

func (w *nodeWorld) handle(dc runtime.DurableContext, n flow.Node, input json.RawMessage) (host.NodeResult, error) {
	cfg, err := node.ConfigFromSpec(n.Name, n.Spec)
	if err != nil {
		return host.NodeResult{}, err
	}
	out, err := w.handler.Handle(nodeDC{DurableContext: dc, w: w}, cfg, input)
	w.mu.Lock()
	w.out = out
	w.runErr = err
	w.mu.Unlock()
	if err != nil {
		return host.NodeResult{}, err
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return host.NodeResult{}, err
	}
	return host.NodeResult{Output: raw, CostMpts: out.CostMpts}, nil
}

// waitSettled waits until the run ends.
func (w *nodeWorld) waitSettled() error {
	select {
	case <-w.doneCh:
		return nil
	case <-time.After(waitBound):
		return fmt.Errorf("timed out waiting for the run to settle")
	}
}

// waitStarted waits until the provider accepted the invocation, the handler
// waits at a gate, or the run ended. The scenarios that report a cost or a
// cancellation do so after the invocation started, so this stops as soon as
// there is something to observe.
func (w *nodeWorld) waitStarted() error {
	accepted := w.provider.acceptedCh
	select {
	case <-accepted:
		return nil
	case <-w.doneCh:
		return nil
	case gate := <-w.gateCh:
		w.mu.Lock()
		w.atGate = gate
		w.mu.Unlock()
		return nil
	case <-time.After(waitBound):
		return fmt.Errorf("timed out waiting for the run to start")
	}
}

// waitLookup waits until the provider answered a lookup that found the
// invocation.
func (w *nodeWorld) waitLookup() error {
	select {
	case <-w.provider.lookupCh:
		return nil
	case <-w.doneCh:
		return fmt.Errorf("the run ended before the node looked the invocation up")
	case <-time.After(waitBound):
		return fmt.Errorf("timed out waiting for the lookup")
	}
}

// waitGate waits until the handler awaits gate.
func (w *nodeWorld) waitGate(gate string) error {
	w.mu.Lock()
	at := w.atGate
	w.mu.Unlock()
	if gateIs(at, gate) {
		return nil
	}
	select {
	case got := <-w.gateCh:
		w.mu.Lock()
		w.atGate = got
		w.mu.Unlock()
		if !gateIs(got, gate) {
			return fmt.Errorf("the run waits at gate %q, want %q", got, gate)
		}
		return nil
	case <-w.doneCh:
		return fmt.Errorf("the run ended before it waited at gate %q", gate)
	case <-time.After(waitBound):
		return fmt.Errorf("timed out waiting for gate %q", gate)
	}
}

// gateIs reports whether name is gate or one of its per-invocation instances:
// the model node appends the client ref so parallel map items wait at distinct
// gates (REQ-MODEL-005).
func gateIs(name, gate string) bool {
	return name == gate || strings.HasPrefix(name, gate+".")
}

// active reports whether the scenario has a run in flight.
func (w *nodeWorld) active() bool { return w != nil && w.running }

// cancelRun cancels the run and waits until the provider received the cancel
// request. The provider's confirmation completes the invocation.
func (w *nodeWorld) cancelRun() error {
	go func() {
		_ = w.rt.Cancel(core.WithTenant(context.Background(), "acme"), w.ref, "")
	}()
	return w.awaitCancel(time.After(waitBound))
}

// awaitCancel waits until the provider received the cancel request or the
// deadline fires.
func (w *nodeWorld) awaitCancel(deadline <-chan time.Time) error {
	select {
	case <-w.provider.cancelCh:
		return nil
	case <-w.doneCh:
		// The runtime ends the run while delivering the cancel, so doneCh and
		// the provider's buffered confirmation can be ready together and the
		// select may pick either. The provider confirms before the handler
		// returns, so drain the confirmation before reporting a lost cancel.
		select {
		case <-w.provider.cancelCh:
			return nil
		default:
		}
		return fmt.Errorf("the run ended before the provider received a cancel request")
	case <-deadline:
		// The provider's confirmation can land just as the bound expires, so
		// cancelCh and the deadline can be ready together and the select may
		// pick either. Drain the confirmation before reporting a timeout.
		if w.cancelDelivered() {
			return nil
		}
		return fmt.Errorf("timed out waiting for the cancel request")
	}
}

// cancelDelivered drains a cancel confirmation the provider already buffered.
func (w *nodeWorld) cancelDelivered() bool {
	select {
	case <-w.provider.cancelCh:
		return true
	default:
		return false
	}
}

// waitAccepted waits until the provider accepted the invocation and the run's
// handler is following it. The second wait is what keeps the invocation
// running from the runtime's point of view: cancelling before the handler
// reaches the follow step can end the run without the provider ever seeing a
// cancel request (MODEL-009-01, EP0-198).
func (w *nodeWorld) waitAccepted() error {
	select {
	case <-w.provider.acceptedCh:
	case <-w.doneCh:
		return fmt.Errorf("the run ended before the provider accepted the invocation")
	case <-time.After(waitBound):
		return fmt.Errorf("timed out waiting for the provider to accept")
	}
	select {
	case <-w.provider.followedCh:
		return nil
	case <-w.doneCh:
		return fmt.Errorf("the run ended before the node followed the invocation")
	case <-time.After(waitBound):
		return fmt.Errorf("timed out waiting for the node to follow the invocation")
	}
}

func (w *nodeWorld) teardown() {
	if !w.running || w.rt == nil {
		return
	}
	select {
	case <-w.doneCh:
	default:
		go func() {
			_ = w.rt.Cancel(core.WithTenant(context.Background(), "acme"), w.ref, "")
		}()
		select {
		case <-w.doneCh:
		case <-time.After(waitBound):
		}
	}
	w.running = false
}

func (w *nodeWorld) clientRef() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.ClientRef
}

func registerNode(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		world(ctx).Node = newNodeWorld()
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		if w := world(ctx).Node; w != nil {
			w.teardown()
		}
		return ctx, nil
	})

	sc.Step(`^a simulated video provider that rejects the first start with a transient error before accepting$`, func(ctx context.Context) error {
		w := world(ctx).Node
		w.ensureProvider(ctx, model.CapVideoGenerate).rejectFirst = true
		return nil
	})
	sc.Step(`^a simulated video provider that accepts the invocation but drops the start response$`, func(ctx context.Context) error {
		w := world(ctx).Node
		w.ensureProvider(ctx, model.CapVideoGenerate).dropResponse = true
		return nil
	})
	sc.Step(`^the provider supports lookup by client ref$`, func(ctx context.Context) error {
		world(ctx).Node.ensureProvider(ctx, model.CapVideoGenerate).lookup = true
		return nil
	})
	sc.Step(`^the invocation's retention window passes before the model node looks it up$`, func(ctx context.Context) error {
		// The adapter's registry keeps an ended invocation for its retention
		// window (1 hour by default); once the window passes, the ref is swept
		// and a lookup finds nothing (MODEL-005-03).
		world(ctx).Node.ensureProvider(ctx, model.CapVideoGenerate).forget = true
		world(ctx).Core.clock.Advance(time.Hour)
		return nil
	})
	sc.Step(`^the provider does not support lookup$`, func(ctx context.Context) error {
		world(ctx).Node.ensureProvider(ctx, model.CapVideoGenerate).lookup = false
		return nil
	})

	sc.Step(`^a model "([^"]*)" whose parameter schema requires "([^"]*)" between (\d+) and (\d+)$`, func(ctx context.Context, id, field string, min, max int) error {
		w := world(ctx).Node
		w.update(ctx, func(d *model.Descriptor) {
			d.ID = id
			d.Params = json.RawMessage(fmt.Sprintf(
				`{"type":"object","properties":{%q:{"type":"integer","minimum":%d,"maximum":%d}},"required":[%q]}`,
				field, min, max, field))
		})
		return nil
	})
	sc.Step(`^the run has a budget of (\d+) milli-points$`, func(ctx context.Context, limit int64) error {
		world(ctx).Node.budget = node.NewMemory(limit)
		return nil
	})

	sc.Step(`^a run invokes "([^"]*)" on it$`, func(ctx context.Context, op string) error {
		w := world(ctx).Node
		if err := w.startRun(ctx, model.Capability(op), ""); err != nil {
			return err
		}
		return w.waitStarted()
	})
	sc.Step(`^a run invokes "([^"]*)" with duration_s (\d+)$`, func(ctx context.Context, op string, duration int) error {
		w := world(ctx).Node
		input := fmt.Sprintf(`{"params":{"duration_s":%d}}`, duration)
		if err := w.startRun(ctx, model.Capability(op), input); err != nil {
			return err
		}
		return w.waitStarted()
	})
	sc.Step(`^a run invoked "([^"]*)" with duration_s (\d+) and the invocation is still running$`, func(ctx context.Context, op string, duration int) error {
		w := world(ctx).Node
		w.ensureProvider(ctx, model.Capability(op)).needConfirm = true
		input := fmt.Sprintf(`{"params":{"duration_s":%d}}`, duration)
		if err := w.startRun(ctx, model.Capability(op), input); err != nil {
			return err
		}
		return w.waitAccepted()
	})

	sc.Step(`^the provider accepted (\d+) invocations? across (\d+) start attempts$`, func(ctx context.Context, accepted, attempts int) error {
		p := world(ctx).Node.provider
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.accepted != accepted || p.starts != attempts {
			return fmt.Errorf("the provider accepted %d across %d start attempts, want %d across %d", p.accepted, p.starts, accepted, attempts)
		}
		return nil
	})
	sc.Step(`^both start attempts carried the same client ref$`, func(ctx context.Context) error {
		p := world(ctx).Node.provider
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.refs) < 2 {
			return fmt.Errorf("the provider saw %d start attempts, want at least 2", len(p.refs))
		}
		for _, ref := range p.refs[1:] {
			if ref != p.refs[0] {
				return fmt.Errorf("start attempts carried refs %v, want one client ref", p.refs)
			}
		}
		return nil
	})
	sc.Step(`^the model node finds the invocation by lookup$`, func(ctx context.Context) error {
		w := world(ctx).Node
		if err := w.waitLookup(); err != nil {
			return err
		}
		p := w.provider
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.lookups == 0 || !p.found {
			return fmt.Errorf("the node made %d lookups and found %v, want a lookup that found the invocation", p.lookups, p.found)
		}
		return nil
	})
	sc.Step(`^the run waits at gate "([^"]*)"$`, func(ctx context.Context, gate string) error {
		return world(ctx).Node.waitGate(gate)
	})
	sc.Step(`^the node fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		if w := world(ctx).Worker; w != nil && w.run != nil {
			return w.assertNodeFails(ctx, code)
		}
		w := world(ctx).Node
		if err := w.waitSettled(); err != nil {
			return err
		}
		w.mu.Lock()
		err := w.runErr
		w.mu.Unlock()
		if err == nil {
			return fmt.Errorf("the node succeeded, want code %s", code)
		}
		if got := core.CodeOf(err); string(got) != code {
			return fmt.Errorf("got code %s, want %s: %w", got, code, err)
		}
		return nil
	})

	sc.Step(`^(\d+) milli-points were reserved before the start$`, func(ctx context.Context, want int64) error {
		w := world(ctx).Node
		w.mu.Lock()
		held, ok := w.heldStart, w.heldSet
		w.mu.Unlock()
		if !ok {
			return fmt.Errorf("the provider never accepted an invocation")
		}
		if held != want {
			return fmt.Errorf("%d milli-points were held when the provider accepted, want %d", held, want)
		}
		return nil
	})
	sc.Step(`^the run is charged (\d+) milli-points and the reservation is closed$`, func(ctx context.Context, want int64) error {
		if w := world(ctx).Worker; w != nil && w.run != nil {
			return w.assertCharged(ctx, want)
		}
		w := world(ctx).Node
		select {
		case <-w.doneCh:
		case <-time.After(waitBound):
			return fmt.Errorf("timed out waiting for the run to end")
		}
		if got := w.budget.Charged(w.clientRef()); got != want {
			return fmt.Errorf("charged %d milli-points, want %d", got, want)
		}
		if !w.budget.Settled(w.clientRef()) || w.budget.HeldTotal() != 0 {
			return fmt.Errorf("the reservation is still open (held %d)", w.budget.HeldTotal())
		}
		return nil
	})
	sc.Step(`^the run is charged (\d+) milli-points and the remaining reservation is released$`, func(ctx context.Context, want int64) error {
		w := world(ctx).Node
		select {
		case <-w.doneCh:
		case <-time.After(waitBound):
			return fmt.Errorf("timed out waiting for the run to end")
		}
		if got := w.budget.Charged(w.clientRef()); got != want {
			return fmt.Errorf("charged %d milli-points, want %d", got, want)
		}
		if w.budget.HeldTotal() != 0 {
			return fmt.Errorf("%d milli-points are still held, want the rest released", w.budget.HeldTotal())
		}
		return nil
	})
	sc.Step(`^the provider confirms cancellation with cost (\d+)(?: milli-points)?$`, func(ctx context.Context, cost int64) error {
		return world(ctx).Node.confirmCancellation(cost)
	})
	sc.Step(`^the provider reports success with cost (\d+) milli-points$`, func(ctx context.Context, cost int64) error {
		world(ctx).Node.provider.succeedWith(cost)
		return nil
	})
	sc.Step(`^the provider reports cancellation with cost (\d+)(?: milli-points)?$`, func(ctx context.Context, cost int64) error {
		return world(ctx).Node.confirmCancellation(cost)
	})
	sc.Step(`^the provider received a cancel request for the invocation$`, func(ctx context.Context) error {
		p := world(ctx).Node.provider
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.cancels == 0 {
			return fmt.Errorf("the provider received no cancel request")
		}
		return nil
	})
	sc.Step(`^the reservation is released$`, func(ctx context.Context) error {
		if w := world(ctx).Worker; w != nil && w.run != nil {
			return w.assertReservationReleased(ctx)
		}
		w := world(ctx).Node
		select {
		case <-w.doneCh:
		case <-time.After(waitBound):
			return fmt.Errorf("timed out waiting for the run to end")
		}
		if w.budget.HeldTotal() != 0 {
			return fmt.Errorf("%d milli-points are still held, want the reservation released", w.budget.HeldTotal())
		}
		if w.budget.ChargedTotal() != 0 {
			return fmt.Errorf("%d milli-points were charged, want none", w.budget.ChargedTotal())
		}
		return nil
	})
}

// succeedWith reports the cost a successful invocation charged and ends it.
func (p *nodeProvider) succeedWith(cost int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, run := range p.runs {
		if run.closed {
			continue
		}
		run.result = model.Result{State: model.StateSucceeded, CostMpts: cost}
		run.closed = true
		close(run.done)
	}
}

// confirmCancellation completes the cancelled invocation the run is waiting
// on with the cost the provider reports.
func (w *nodeWorld) confirmCancellation(cost int64) error {
	if w.provider == nil {
		return fmt.Errorf("no provider is simulated")
	}
	w.provider.mu.Lock()
	var handle model.Handle
	for _, run := range w.provider.runs {
		handle = run.handle
		break
	}
	w.provider.mu.Unlock()
	if handle.ClientRef == "" {
		return fmt.Errorf("the provider has no invocation to confirm")
	}
	w.provider.confirm(handle, cost)
	return nil
}
