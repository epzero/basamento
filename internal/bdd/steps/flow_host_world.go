package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

const maxHandlerGuardRuns = 100

// hostWorld drives the flow host against a recording fake DurableContext. The
// fake records every durable call in order, replays a scripted history and
// advances only with the scenario's clock, so host.feature can read a run's
// calls back exactly.
type hostWorld struct {
	mu       sync.Mutex
	cond     *sync.Cond
	catalog  *hostCatalog
	host     *host.Host
	hostOpts []host.Option
	dc       *hostFake

	flow     *flow.IR
	defs     map[string]map[int]*flow.IR
	versions map[string]map[int]runtime.FlowRef

	behaviors    map[string]*hostBehavior
	handlerKinds map[string]bool
	handlerCalls []hostHandlerCall

	done        chan struct{}
	result      json.RawMessage
	err         error
	callsAtDone int

	firstJournal []hostJournalEntry

	replayAcceptsFailure bool

	costObservedStep  string // node whose cost FLOW-059 observes via run.cost
	costObserverNode  string // downstream handler that receives run.cost
}

type hostHandlerCall struct {
	node  string
	input json.RawMessage
}

type hostBehavior struct {
	output   json.RawMessage
	costMpts int64
	err      error
	blocking bool
	released bool
	step     string
	called   bool
	result   json.RawMessage
}

func newHostWorld() *hostWorld {
	w := &hostWorld{
		catalog:      newHostCatalog(),
		defs:         map[string]map[int]*flow.IR{},
		versions:     map[string]map[int]runtime.FlowRef{},
		behaviors:    map[string]*hostBehavior{},
		handlerKinds: map[string]bool{},
	}
	w.cond = sync.NewCond(&w.mu)
	w.host = host.New(w.catalog)
	return w
}

func (w *hostWorld) newFlow(name string, version int) *flow.IR {
	def := &flow.IR{
		Name:    name,
		Version: version,
		State: flow.StateSpec{
			Schema: json.RawMessage(`{"type":"object"}`),
		},
		Nodes: map[string]flow.Node{},
		Edges: []flow.Edge{},
	}
	w.flow = def
	if w.defs[name] == nil {
		w.defs[name] = map[int]*flow.IR{}
	}
	w.defs[name][version] = def
	return def
}

func (w *hostWorld) ensureFlow() *flow.IR {
	if w.flow == nil {
		return w.newFlow("flow", 1)
	}
	return w.flow
}

func (w *hostWorld) register(def *flow.IR) runtime.FlowRef {
	ref := w.catalog.register(def)
	if w.defs[def.Name] == nil {
		w.defs[def.Name] = map[int]*flow.IR{}
	}
	w.defs[def.Name][def.Version] = def
	if w.versions[def.Name] == nil {
		w.versions[def.Name] = map[int]runtime.FlowRef{}
	}
	w.versions[def.Name][def.Version] = ref
	return ref
}

func (w *hostWorld) refFor(def *flow.IR) runtime.FlowRef {
	if refs := w.versions[def.Name]; refs != nil {
		if ref, ok := refs[def.Version]; ok {
			return ref
		}
	}
	return w.register(def)
}

func (w *hostWorld) ensureHandler(kind string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.handlerKinds[kind] {
		return
	}
	w.handlerKinds[kind] = true
	w.host.RegisterHandler(kind, w.handle)
}

func (w *hostWorld) behavior(node string) *hostBehavior {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.behaviors[node] == nil {
		w.behaviors[node] = &hostBehavior{}
	}
	return w.behaviors[node]
}

func (w *hostWorld) handle(dc runtime.DurableContext, node flow.Node, input json.RawMessage) (host.NodeResult, error) {
	w.mu.Lock()
	b := w.behaviors[node.Name]
	runs := 0
	for _, c := range w.handlerCalls {
		if c.node == node.Name {
			runs++
		}
	}
	if runs >= maxHandlerGuardRuns {
		w.mu.Unlock()
		return host.NodeResult{}, core.NewError(core.CodeInternal,
			fmt.Sprintf("flow host test: node %q ran more than %d times: a guard did not stop the loop", node.Name, maxHandlerGuardRuns),
			nil)
	}
	w.handlerCalls = append(w.handlerCalls, hostHandlerCall{node: node.Name, input: append(json.RawMessage(nil), input...)})
	if b != nil {
		b.called = true
	}
	w.mu.Unlock()
	if b == nil {
		return host.NodeResult{}, fmt.Errorf("flow host test: no behavior for node %q", node.Name)
	}
	if b.step != "" {
		var out json.RawMessage
		if err := dc.Step(b.step, input, runtime.StepOptions{}).Get(&out); err != nil {
			return host.NodeResult{}, err
		}
		w.mu.Lock()
		b.result = append(json.RawMessage(nil), out...)
		w.mu.Unlock()
		return host.NodeResult{Output: out}, nil
	}
	if b.blocking && w.dc != nil {
		dc := w.dc
		for {
			dc.mu.Lock()
			w.mu.Lock()
			released := b.released
			w.mu.Unlock()
			if released || dc.finished {
				dc.parked = false
				dc.cond.Broadcast()
				dc.mu.Unlock()
				break
			}
			dc.parked = true
			dc.cond.Broadcast()
			dc.cond.Wait()
			dc.parked = false
			dc.cond.Broadcast()
			dc.mu.Unlock()
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return host.NodeResult{Output: b.output, CostMpts: b.costMpts}, b.err
}

func (w *hostWorld) declared() bool {
	return w.flow != nil
}

func (w *hostWorld) startDeclared() error {
	return w.start(`{}`)
}

func (w *hostWorld) started() bool {
	return w.done != nil
}

func (w *hostWorld) nodeRuns(node string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.behaviors[node] == nil {
		return 0, fmt.Errorf("node %q has no handler behavior in the host world", node)
	}
	n := 0
	for _, c := range w.handlerCalls {
		if c.node == node {
			n++
		}
	}
	return n, nil
}

func (w *hostWorld) failure() (code, message string, ok bool) {
	if w.err == nil {
		return "", "", false
	}
	var pe *core.Error
	if errors.As(w.err, &pe) {
		return string(pe.Code), pe.Message, true
	}
	return string(core.CodeOf(w.err)), w.err.Error(), true
}

func (w *hostWorld) handlerReturns(node, output string) error {
	b := w.behavior(node)
	var value json.RawMessage
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		return fmt.Errorf("handler output %q: %w", output, err)
	}
	w.mu.Lock()
	b.output = value
	if b.blocking && !b.released {
		b.released = true
		w.cond.Broadcast()
	}
	w.mu.Unlock()
	if w.dc != nil {
		w.dc.mu.Lock()
		w.dc.wake()
		w.dc.mu.Unlock()
	}
	return nil
}

func (w *hostWorld) handlerFails(node string, err error) {
	b := w.behavior(node)
	w.mu.Lock()
	b.err = err
	if b.blocking && !b.released {
		b.released = true
		w.cond.Broadcast()
	}
	w.mu.Unlock()
	if w.dc != nil {
		w.dc.mu.Lock()
		w.dc.wake()
		w.dc.mu.Unlock()
	}
}

func (w *hostWorld) start(input string) error {
	if w.flow == nil {
		return fmt.Errorf("no flow declared")
	}
	return w.startRef(w.refFor(w.flow), input)
}

func (w *hostWorld) startVersion(name string, version int, input string) error {
	defs := w.defs[name]
	if defs == nil || defs[version] == nil {
		return fmt.Errorf("flow %q version %d is not declared", name, version)
	}
	return w.startRef(w.refFor(defs[version]), input)
}

func (w *hostWorld) startRef(ref runtime.FlowRef, input string) error {
	if w.done != nil {
		return fmt.Errorf("a run is already in progress")
	}
	w.done = make(chan struct{})
	w.result = nil
	w.err = nil
	go func() {
		w.result, w.err = w.host.Run(w.dc, ref, json.RawMessage(input))
		w.callsAtDone = len(w.dc.callLog())
		close(w.done)
		w.dc.markFinished()
	}()
	return w.settle()
}

// settle waits until the run goroutine has parked on the fake or finished.
func (w *hostWorld) settle() error {
	if w.dc == nil {
		return nil
	}
	w.dc.waitParkedOrFinished()
	select {
	case <-w.done:
		return nil
	default:
		return nil
	}
}

func (w *hostWorld) waitDone() error {
	if w.done == nil {
		return fmt.Errorf("no run in progress")
	}
	if w.dc != nil {
		w.dc.waitParkedOrFinished()
		w.dc.mu.Lock()
		finished := w.dc.finished
		w.dc.mu.Unlock()
		if !finished {
			return fmt.Errorf("run is waiting")
		}
	}
	<-w.done
	return nil
}

// abortInFlightRun ends a background Run left waiting (for example at "the run is
// waiting") so the scenario goroutine cannot leak into the next scenario.
func (w *hostWorld) abortInFlightRun() {
	if w.done == nil {
		return
	}
	select {
	case <-w.done:
		return
	default:
	}
	if w.dc != nil {
		w.dc.abort()
		w.dc.waitFinished()
	}
	<-w.done
}

func (w *hostWorld) timePassed(_ context.Context, duration time.Duration) error {
	if w.dc == nil || w.done == nil {
		return nil
	}
	w.dc.advance(duration)
	return w.settle()
}

// --- flow building helpers -------------------------------------------------

func (w *hostWorld) addNode(id, kind, spec, in, out string) {
	def := w.ensureFlow()
	node := flow.Node{Kind: kind, In: in, Out: out}
	if spec != "" {
		node.Spec = json.RawMessage(spec)
	}
	def.Nodes[id] = node
}

func (w *hostWorld) addEdge(from, to, when string) {
	def := w.ensureFlow()
	def.Edges = append(def.Edges, flow.Edge{From: from, To: to, When: when})
}

func (w *hostWorld) stateSchema(schema string) {
	w.ensureFlow().State.Schema = json.RawMessage(schema)
}

func hostObjectSchema(properties map[string]string) string {
	keys := make([]string, 0, len(properties))
	for key := range properties {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(properties))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%q:%s", key, properties[key]))
	}
	return `{"type":"object","properties":{` + strings.Join(parts, ",") + `}}`
}

func setSpec(key string, value any) string {
	raw, _ := json.Marshal(map[string]any{key: value})
	return string(raw)
}

func (w *hostWorld) registerHandledKind(kind string) {
	w.ensureHandler(kind)
}

func (w *hostWorld) rebuildHost(opts ...host.Option) {
	w.hostOpts = append([]host.Option(nil), opts...)
	w.host = host.New(w.catalog, w.hostOpts...)
	for kind := range w.handlerKinds {
		w.mu.Lock()
		registered := w.handlerKinds[kind]
		w.mu.Unlock()
		if registered {
			w.host.RegisterHandler(kind, w.handle)
		}
	}
}

func (c *hostCatalog) registerContentAlias(alias string, def *flow.IR) {
	hash, err := ir.Hash(def)
	if err != nil {
		panic(err)
	}
	c.mu.Lock()
	c.byHash[alias] = def
	c.byHash[hash] = def
	c.mu.Unlock()
}

func (c *hostCatalog) setPinSubstitute(pinnedLabel string, def *flow.IR) {
	resolved, err := c.contentHash(pinnedLabel)
	if err != nil {
		panic(err)
	}
	c.mu.Lock()
	c.pinSubstitute[resolved] = def
	c.mu.Unlock()
}

// contentHash resolves a catalog content-hash label to the definition's ir.Hash.
func (c *hostCatalog) contentHash(label string) (string, error) {
	c.mu.RLock()
	def, ok := c.byHash[label]
	c.mu.RUnlock()
	if !ok {
		return label, nil
	}
	return ir.Hash(def)
}

func (c *hostCatalog) pinnedRef(name, pinLabel string) (runtime.FlowRef, error) {
	hash, err := c.contentHash(pinLabel)
	if err != nil {
		return runtime.FlowRef{}, err
	}
	return runtime.FlowRef{Name: name, Hash: hash}, nil
}

var replayFlowStep = regexp.MustCompile(`^a flow with node "([^"]*)" of kind "tool" with node spec (\{.*\}), then node "([^"]*)" of kind "wait" for "([^"]*)", then node "([^"]*)" of kind "agent" whose handler calls step "([^"]*)"$`)

// declareReplayFlow is the FLOW-037-01 flow declaration; flow_builder's catch-all routes matching text here.
func declareReplayFlow(ctx context.Context, first, firstSpec, second, duration, third, step string) error {
	w := world(ctx).Host
	w.newFlow("replay", 1)
	w.stateSchema(hostObjectSchema(map[string]string{third: `{"type":"object"}`}))
	w.addNode(first, "tool", firstSpec, "", "")
	w.addNode(second, "wait", fmt.Sprintf(`{"duration":%q}`, duration), "", "")
	w.addNode(third, "agent", "", "", third)
	w.addEdge(ir.Start, first, "")
	w.addEdge(first, second, "")
	w.addEdge(second, third, "")
	w.registerHandledKind("agent")
	w.behavior(third).step = step
	return nil
}
