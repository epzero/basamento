package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

// hostWorld drives the flow host against a recording fake DurableContext. The
// fake records every durable call in order, replays a scripted history and
// advances only with the scenario's clock, so host.feature can read a run's
// calls back exactly.
type hostWorld struct {
	mu      sync.Mutex
	catalog *hostCatalog
	host    *host.Host
	dc      *hostFake

	flow     *flow.IR
	defs     map[string]map[int]*flow.IR
	versions map[string]map[int]runtime.FlowRef

	behaviors    map[string]*hostBehavior
	handlerKinds map[string]bool
	handlerCalls []hostHandlerCall

	activity    chan struct{}
	done        chan struct{}
	result      json.RawMessage
	err         error
	callsAtDone int

	firstCalls  []hostCall
	replayCalls []hostCall
}

type hostHandlerCall struct {
	node  string
	input json.RawMessage
}

type hostBehavior struct {
	output   json.RawMessage
	err      error
	blocking bool
	release  chan struct{}
	step     string
	called   bool
	released bool
	result   json.RawMessage
}

func newHostWorld() *hostWorld {
	w := &hostWorld{
		catalog:      newHostCatalog(),
		defs:         map[string]map[int]*flow.IR{},
		versions:     map[string]map[int]runtime.FlowRef{},
		behaviors:    map[string]*hostBehavior{},
		handlerKinds: map[string]bool{},
		activity:     make(chan struct{}, 1),
	}
	w.host = host.New(w.catalog)
	return w
}

func (w *hostWorld) signal() {
	select {
	case w.activity <- struct{}{}:
	default:
	}
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

func (w *hostWorld) handle(dc runtime.DurableContext, node flow.Node, input json.RawMessage) (json.RawMessage, error) {
	w.mu.Lock()
	b := w.behaviors[node.Name]
	w.handlerCalls = append(w.handlerCalls, hostHandlerCall{node: node.Name, input: append(json.RawMessage(nil), input...)})
	if b != nil {
		b.called = true
	}
	w.mu.Unlock()
	w.signal()
	if b == nil {
		return nil, fmt.Errorf("flow host test: no behavior for node %q", node.Name)
	}
	if b.step != "" {
		var out json.RawMessage
		if err := dc.Step(b.step, input, runtime.StepOptions{}).Get(&out); err != nil {
			return nil, err
		}
		w.mu.Lock()
		b.result = append(json.RawMessage(nil), out...)
		w.mu.Unlock()
		return out, nil
	}
	if b.blocking {
		<-b.release
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return b.output, b.err
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
		close(b.release)
		b.released = true
	}
	w.mu.Unlock()
	return nil
}

func (w *hostWorld) handlerFails(node string, err error) {
	b := w.behavior(node)
	w.mu.Lock()
	b.err = err
	if b.blocking && !b.released {
		close(b.release)
		b.released = true
	}
	w.mu.Unlock()
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
		w.signal()
		close(w.done)
	}()
	return w.settle()
}

// settle waits until the run finishes or pauses on a durable call. The fake
// signals activity for every call and the handler does the same for every
// invocation, so a quiet period means the run is blocked in Select.
func (w *hostWorld) settle() error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	quiet := time.NewTimer(20 * time.Millisecond)
	quiet.Stop()
	active := false
	for {
		select {
		case <-w.done:
			return nil
		case <-w.activity:
			active = true
			if !quiet.Stop() {
				select {
				case <-quiet.C:
				default:
				}
			}
			quiet.Reset(20 * time.Millisecond)
		case <-quiet.C:
			if active {
				return nil
			}
		case <-deadline.C:
			return fmt.Errorf("run did not settle")
		}
	}
}

func (w *hostWorld) waitDone() error {
	select {
	case <-w.done:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("run did not finish")
	}
}

// --- recording fake DurableContext ----------------------------------------

type hostCall struct {
	kind     string
	name     string
	input    json.RawMessage
	duration time.Duration
	gate     runtime.GateSpec
	child    runtime.FlowRef
}

type hostScript struct {
	result json.RawMessage
	err    error
}

type hostFuture struct {
	done   bool
	result json.RawMessage
	err    error
	dc     *hostFake
}

func (f *hostFuture) Get(out any) error {
	f.dc.mu.Lock()
	for !f.done {
		f.dc.cond.Wait()
	}
	f.dc.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if out == nil || len(f.result) == 0 {
		return nil
	}
	return json.Unmarshal(f.result, out)
}

func (f *hostFuture) Ready() bool {
	f.dc.mu.Lock()
	defer f.dc.mu.Unlock()
	return f.done
}

type hostTimer struct {
	deadline time.Time
	future   *hostFuture
	gate     bool
}

type hostFake struct {
	mu        sync.Mutex
	cond      *sync.Cond
	now       time.Time
	calls     []hostCall
	notify    chan struct{}
	steps     map[string]hostScript
	gates     map[string]*hostFuture
	timers    []hostTimer
	gateDone  map[string]json.RawMessage
	childDone map[string]json.RawMessage
	emits     int
	checks    int
	instant   bool
	replaying bool
	executed  map[string]int
}

func newHostFake(notify chan struct{}) *hostFake {
	d := &hostFake{
		now:       time.Unix(1_700_000_000, 0).UTC(),
		notify:    notify,
		steps:     map[string]hostScript{},
		gates:     map[string]*hostFuture{},
		gateDone:  map[string]json.RawMessage{},
		childDone: map[string]json.RawMessage{},
		executed:  map[string]int{},
	}
	d.cond = sync.NewCond(&d.mu)
	return d
}

func (d *hostFake) record(c hostCall) {
	d.mu.Lock()
	d.calls = append(d.calls, c)
	d.mu.Unlock()
	select {
	case d.notify <- struct{}{}:
	default:
	}
}

func (d *hostFake) Context() context.Context { return context.Background() }
func (d *hostFake) Now() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.now
}

var fakeID int

func (d *hostFake) NewID(kind string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	fakeID++
	return fmt.Sprintf("%s-%d", kind, fakeID)
}

func (d *hostFake) Rand(string) *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }

func (d *hostFake) Step(name string, in any, _ runtime.StepOptions) runtime.Future {
	d.record(hostCall{kind: "step", name: name, input: rawArgument(in)})
	d.mu.Lock()
	defer d.mu.Unlock()
	script := d.steps[name]
	if !d.replaying {
		d.executed[name]++
	}
	return &hostFuture{dc: d, done: true, result: script.result, err: script.err}
}

func (d *hostFake) Child(ref runtime.FlowRef, in any, _ runtime.ChildOptions) runtime.Future {
	d.record(hostCall{kind: "child", child: ref, input: rawArgument(in)})
	d.mu.Lock()
	defer d.mu.Unlock()
	script := d.steps["child:"+ref.Name]
	if len(script.result) > 0 && script.err == nil {
		d.childDone[ref.Name] = script.result
	}
	return &hostFuture{dc: d, done: true, result: script.result, err: script.err}
}

func (d *hostFake) Sleep(duration time.Duration) runtime.Future {
	d.record(hostCall{kind: "timer", duration: duration})
	d.mu.Lock()
	defer d.mu.Unlock()
	f := &hostFuture{dc: d}
	if d.instant {
		f.done = true
		return f
	}
	d.timers = append(d.timers, hostTimer{deadline: d.now.Add(duration), future: f})
	return f
}

func (d *hostFake) Await(spec runtime.GateSpec) runtime.Future {
	d.record(hostCall{kind: "gate", gate: spec})
	d.mu.Lock()
	defer d.mu.Unlock()
	f := &hostFuture{dc: d}
	d.gates[spec.Name] = f
	if spec.Timeout > 0 {
		d.timers = append(d.timers, hostTimer{deadline: d.now.Add(spec.Timeout), future: f, gate: true})
	}
	return f
}

func (d *hostFake) Select(futures ...runtime.Future) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for {
		for i, candidate := range futures {
			if candidate.(*hostFuture).done {
				return i, nil
			}
		}
		d.cond.Wait()
	}
}

func (d *hostFake) Emit(runtime.ProgressEvent) error {
	d.mu.Lock()
	d.emits++
	d.mu.Unlock()
	d.record(hostCall{kind: "emit"})
	return nil
}

func (d *hostFake) Checkpoint(runtime.SnapshotRef) error {
	d.mu.Lock()
	d.checks++
	d.mu.Unlock()
	d.record(hostCall{kind: "checkpoint"})
	return nil
}

func (d *hostFake) Capabilities() runtime.Caps { return runtime.Caps{} }

func (d *hostFake) advance(duration time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.now = d.now.Add(duration)
	kept := d.timers[:0]
	for _, timer := range d.timers {
		if timer.deadline.After(d.now) {
			kept = append(kept, timer)
			continue
		}
		timer.future.done = true
		if timer.gate {
			timer.future.err = core.NewError(core.CodeTimeout, "gate timed out", nil)
		}
	}
	d.timers = kept
	d.cond.Broadcast()
}

func (d *hostFake) resolveGate(name string, payload json.RawMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.gates[name]
	if f != nil {
		f.done = true
		f.result = payload
		d.gateDone[name] = payload
		d.cond.Broadcast()
	}
}

func (d *hostFake) callLog() []hostCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]hostCall(nil), d.calls...)
}

func (d *hostFake) executeCount(name string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.executed[name]
}

func (d *hostFake) clone() *hostFake {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := newHostFake(d.notify)
	out.now = d.now
	out.instant = d.instant
	out.replaying = true
	for name, script := range d.steps {
		out.steps[name] = script
	}
	return out
}

func rawArgument(in any) json.RawMessage {
	switch value := in.(type) {
	case nil:
		return nil
	case json.RawMessage:
		return append(json.RawMessage(nil), value...)
	default:
		raw, _ := json.Marshal(value)
		return raw
	}
}

// --- definition catalog ----------------------------------------------------

type hostCatalog struct {
	mu     sync.RWMutex
	byHash map[string]*flow.IR
	latest map[string]*flow.IR
}

func newHostCatalog() *hostCatalog {
	return &hostCatalog{byHash: map[string]*flow.IR{}, latest: map[string]*flow.IR{}}
}

func (c *hostCatalog) register(def *flow.IR) runtime.FlowRef {
	hash, err := ir.Hash(def)
	if err != nil {
		panic(err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byHash[hash] = def
	c.latest[def.Name] = def
	return runtime.FlowRef{Name: def.Name, Hash: hash}
}

func (c *hostCatalog) Load(_ context.Context, ref runtime.FlowRef) (*flow.IR, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if ref.Hash != "" {
		def, ok := c.byHash[ref.Hash]
		if !ok {
			return nil, core.NewError(core.CodeNotFound, "definition not found", nil)
		}
		return def, nil
	}
	def, ok := c.latest[ref.Name]
	if !ok {
		return nil, core.NewError(core.CodeNotFound, "definition not found", nil)
	}
	return def, nil
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

// --- step definitions ------------------------------------------------------

func registerFlowHost(sc *godog.ScenarioContext) {
	sc.Step(`^a recording fake durable context$`, func(ctx context.Context) error {
		w := world(ctx).Host
		w.dc = newHostFake(w.activity)
		return nil
	})

	// FLOW-027-01: the pinned definition.
	sc.Step(`^flow "([^"]*)" version (\d+) whose node "([^"]*)" of kind "([^"]*)" with output "([^"]*)" has a registered node handler and precedes node "([^"]*)" of kind "set" setting "([^"]*)" to (-?\d+)$`,
		func(ctx context.Context, name string, version int, first, kind, out, second, key string, value int) error {
			w := world(ctx).Host
			def := w.newFlow(name, version)
			def.State.Schema = json.RawMessage(hostObjectSchema(map[string]string{
				"plan": `{"type":"object"}`,
				key:    `{"type":"integer"}`,
			}))
			def.Nodes[first] = flow.Node{Kind: kind, Out: out}
			def.Nodes[second] = flow.Node{Kind: "set", Spec: json.RawMessage(setSpec(key, value))}
			def.Edges = []flow.Edge{{From: ir.Start, To: first}, {From: first, To: second}}
			w.ensureHandler(kind)
			b := w.behavior(first)
			b.blocking = true
			b.release = make(chan struct{})
			return nil
		})

	sc.Step(`^flow "([^"]*)" version (\d+) is registered$`, func(ctx context.Context, name string, version int) error {
		w := world(ctx).Host
		defs := w.defs[name]
		if defs == nil || defs[version] == nil {
			return fmt.Errorf("flow %q version %d is not declared", name, version)
		}
		w.register(defs[version])
		return nil
	})

	sc.Step(`^a run of "([^"]*)" version (\d+) starts and waits at the node "([^"]*)"$`,
		func(ctx context.Context, name string, version int, node string) error {
			w := world(ctx).Host
			if err := w.startVersion(name, version, `{}`); err != nil {
				return err
			}
			w.mu.Lock()
			b := w.behaviors[node]
			called := b != nil && b.called
			w.mu.Unlock()
			if !called {
				return fmt.Errorf("handler for node %q was not called", node)
			}
			return nil
		})

	sc.Step(`^flow "([^"]*)" version (\d+), where node "([^"]*)" with output "([^"]*)" precedes node "([^"]*)" of kind "set" setting "([^"]*)" to (-?\d+), is registered$`,
		func(ctx context.Context, name string, version int, first, out, second, key string, value int) error {
			w := world(ctx).Host
			def := &flow.IR{
				Name:    name,
				Version: version,
				State: flow.StateSpec{Schema: json.RawMessage(hostObjectSchema(map[string]string{
					"plan": `{"type":"object"}`,
					key:    `{"type":"integer"}`,
				}))},
				Nodes: map[string]flow.Node{
					first:  {Kind: "agent", Out: out},
					second: {Kind: "set", Spec: json.RawMessage(setSpec(key, value))},
				},
				Edges: []flow.Edge{{From: ir.Start, To: first}, {From: first, To: second}},
			}
			w.defs[name][version] = def
			w.register(def)
			return nil
		})

	sc.Step(`^the node handler of "([^"]*)" returns (\{.*\})$`, func(ctx context.Context, node, output string) error {
		return world(ctx).Host.handlerReturns(node, output)
	})

	sc.Step(`^the run completes with state (\{.*\})$`, func(ctx context.Context, want string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err != nil {
			return fmt.Errorf("run failed: %w", w.err)
		}
		if !jsonEquivalent(w.result, json.RawMessage(want)) {
			return fmt.Errorf("state %s, want %s", w.result, want)
		}
		return nil
	})

	sc.Step(`^the run completes with state ([^ ]+) = (\{.*\})$`, func(ctx context.Context, key, want string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err != nil {
			return fmt.Errorf("run failed: %w", w.err)
		}
		var state map[string]json.RawMessage
		if err := json.Unmarshal(w.result, &state); err != nil {
			return fmt.Errorf("state %s: %w", w.result, err)
		}
		if !jsonEquivalent(state[key], json.RawMessage(want)) {
			return fmt.Errorf("state.%s is %s, want %s", key, state[key], want)
		}
		return nil
	})

	// FLOW-028: a kind without a handler.
	sc.Step(`^flow "([^"]*)" whose node "([^"]*)" of kind "([^"]*)" with node spec (\{.*\}) precedes the node "([^"]*)" of kind "([^"]*)"$`,
		func(ctx context.Context, name, first, firstKind, spec, second, secondKind string) error {
			w := world(ctx).Host
			def := w.newFlow(name, 1)
			def.Nodes[first] = flow.Node{Kind: firstKind, Spec: json.RawMessage(spec)}
			def.Nodes[second] = flow.Node{Kind: secondKind}
			def.Edges = []flow.Edge{{From: ir.Start, To: first}, {From: first, To: second}}
			return nil
		})

	sc.Step(`^no node handler is registered for the kind "([^"]*)"$`, func(ctx context.Context, kind string) error {
		w := world(ctx).Host
		w.mu.Lock()
		registered := w.handlerKinds[kind]
		w.mu.Unlock()
		if registered {
			return fmt.Errorf("a node handler is registered for kind %q", kind)
		}
		return nil
	})

	sc.Step(`^a run of "([^"]*)" starts with input (\{.*\})$`, func(ctx context.Context, name, input string) error {
		w := world(ctx).Host
		defs := w.defs[name]
		if defs == nil {
			return fmt.Errorf("flow %q is not declared", name)
		}
		var def *flow.IR
		for _, candidate := range defs {
			def = candidate
		}
		return w.startRef(w.refFor(def), input)
	})

	sc.Step(`^the run fails with code "([^"]*)" naming node "([^"]*)"$`, func(ctx context.Context, code, node string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err == nil {
			return fmt.Errorf("run succeeded, want failure %q", code)
		}
		var platform *core.Error
		if !errors.As(w.err, &platform) {
			return fmt.Errorf("error is not a *core.Error: %w", w.err)
		}
		if string(platform.Code) != code {
			return fmt.Errorf("code %s, want %s: %s", platform.Code, code, platform.Message)
		}
		if got, _ := platform.Details["node"].(string); got != node {
			return fmt.Errorf("error details name node %q, want %q", got, node)
		}
		return nil
	})

	sc.Step(`^the run fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err == nil {
			return fmt.Errorf("run succeeded, want failure %q", code)
		}
		if got := string(core.CodeOf(w.err)); got != code {
			return fmt.Errorf("code %s, want %s: %w", got, code, w.err)
		}
		return nil
	})

	sc.Step(`^the run made no step, timer, gate wait, child run or durable event$`, func(ctx context.Context) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		for _, call := range w.dc.callLog() {
			switch call.kind {
			case "step", "timer", "gate", "child", "emit", "checkpoint":
				return fmt.Errorf("run made a %s call: %+v", call.kind, call)
			}
		}
		return nil
	})

	// FLOW-029/030: an effectful node with input and output mapping.
	sc.Step(`^a flow whose node "([^"]*)" of kind "([^"]*)" maps in the input from "([^"]*)", writes output to "([^"]*)", and has the node spec (\{.*\})$`,
		func(ctx context.Context, node, kind, in, out, spec string) error {
			w := world(ctx).Host
			w.newFlow("mapped", 1)
			w.stateSchema(hostObjectSchema(map[string]string{"draft": `{"type":"object"}`, out: `{"type":"object"}`}))
			w.addNode(node, kind, spec, in, out)
			w.addEdge(ir.Start, node, "")
			return nil
		})

	sc.Step(`^the node "([^"]*)" maps in the input from "([^"]*)", writes output to "([^"]*)", and has the node spec (\{.*\})$`,
		func(ctx context.Context, node, in, out, spec string) error {
			w := world(ctx).Host
			def := w.ensureFlow()
			n := def.Nodes[node]
			n.In, n.Out, n.Spec = in, out, json.RawMessage(spec)
			def.Nodes[node] = n
			return nil
		})

	sc.Step(`^the step "([^"]*)" returns (\{.*\})$`, func(ctx context.Context, name, result string) error {
		w := world(ctx).Host
		w.dc.steps[name] = hostScript{result: json.RawMessage(result)}
		return nil
	})

	sc.Step(`^the step "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, name, code string) error {
		w := world(ctx).Host
		w.dc.steps[name] = hostScript{err: core.NewError(core.Code(code), "step failed", nil)}
		return nil
	})

	sc.Step(`^a run of the flow starts with input (\{.*\})$`, func(ctx context.Context, input string) error {
		return world(ctx).Host.start(input)
	})

	sc.Step(`^a run of the flow has started$`, func(ctx context.Context) error {
		return world(ctx).Host.start(`{}`)
	})

	sc.Step(`^the run made exactly one step, named "([^"]*)", with input (\{.*\})$`,
		func(ctx context.Context, name, want string) error {
			w := world(ctx).Host
			if err := w.waitDone(); err != nil {
				return err
			}
			steps := w.steps()
			if len(steps) != 1 {
				return fmt.Errorf("run made %d steps, want 1: %+v", len(steps), steps)
			}
			if steps[0].name != name {
				return fmt.Errorf("step name %q, want %q", steps[0].name, name)
			}
			if !jsonEquivalent(steps[0].input, json.RawMessage(want)) {
				return fmt.Errorf("step input %s, want %s", steps[0].input, want)
			}
			return nil
		})

	sc.Step(`^a flow whose node "([^"]*)" of kind "([^"]*)" has a registered node handler$`,
		func(ctx context.Context, node, kind string) error {
			w := world(ctx).Host
			if w.flow == nil {
				w.newFlow("handled", 1)
			}
			w.addNode(node, kind, "", "", "")
			if !hasEdgeFrom(w.flow, ir.Start, node) {
				w.addEdge(ir.Start, node, "")
			}
			w.registerHandledKind(kind)
			w.behavior(node)
			return nil
		})

	sc.Step(`^the node handler of "([^"]*)" was called with node "([^"]*)",? and input (\{.*\})$`,
		func(ctx context.Context, handler, node, want string) error {
			w := world(ctx).Host
			if err := w.waitDone(); err != nil {
				return err
			}
			w.mu.Lock()
			calls := append([]hostHandlerCall(nil), w.handlerCalls...)
			w.mu.Unlock()
			for _, call := range calls {
				if call.node != node {
					continue
				}
				if !jsonEquivalent(call.input, json.RawMessage(want)) {
					return fmt.Errorf("handler input %s, want %s", call.input, want)
				}
				return nil
			}
			return fmt.Errorf("node handler of %q was not called", handler)
		})

	// FLOW-031: wait nodes.
	sc.Step(`^a flow whose node "([^"]*)" of kind "wait" has the node spec (\{.*\})$`,
		func(ctx context.Context, node, spec string) error {
			w := world(ctx).Host
			w.newFlow("wait", 1)
			w.addNode(node, "wait", spec, "", "")
			w.addEdge(ir.Start, node, "")
			return nil
		})

	sc.Step(`^the run made a timer for (\S+)$`, func(ctx context.Context, want string) error {
		w := world(ctx).Host
		duration, err := time.ParseDuration(want)
		if err != nil {
			return err
		}
		timers := w.timers()
		if len(timers) != 1 || timers[0].duration != duration {
			return fmt.Errorf("timers %+v, want one %s", timers, want)
		}
		return nil
	})

	sc.Step(`^the run made a timer for (\S+) and a timer for (\S+)$`, func(ctx context.Context, first, second string) error {
		w := world(ctx).Host
		one, err := time.ParseDuration(first)
		if err != nil {
			return err
		}
		two, err := time.ParseDuration(second)
		if err != nil {
			return err
		}
		timers := w.timers()
		if len(timers) != 2 || timers[0].duration != one || timers[1].duration != two {
			return fmt.Errorf("timers %+v, want %s and %s", timers, first, second)
		}
		return nil
	})

	sc.Step(`^the run is waiting$`, func(ctx context.Context) error {
		w := world(ctx).Host
		select {
		case <-w.done:
			return fmt.Errorf("run finished, want it waiting")
		default:
			return nil
		}
	})

	sc.Step(`^the run completed node "([^"]*)" with no result$`, func(ctx context.Context, node string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err != nil {
			return fmt.Errorf("run failed: %w", w.err)
		}
		if _, ok := w.nodeResult(node); ok {
			return fmt.Errorf("node %q has a result, want none", node)
		}
		return nil
	})

	sc.Step(`^the run completed node "([^"]*)" with the result (\{.*\})$`, func(ctx context.Context, node, want string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err != nil {
			return fmt.Errorf("run failed: %w", w.err)
		}
		got, ok := w.nodeResult(node)
		if !ok {
			return fmt.Errorf("node %q has no recorded result", node)
		}
		if !jsonEquivalent(got, json.RawMessage(want)) {
			return fmt.Errorf("node %q result %s, want %s", node, got, want)
		}
		return nil
	})

	// FLOW-032: gates.
	sc.Step(`^a flow whose node "([^"]*)" of kind "gate" has the node spec (\{.*\})$`,
		func(ctx context.Context, node, spec string) error {
			w := world(ctx).Host
			w.newFlow("gate", 1)
			w.addNode(node, "gate", spec, "", "")
			w.addEdge(ir.Start, node, "")
			return nil
		})

	sc.Step(`^the gate "([^"]*)" is resolved with (\{.*\})$`, func(ctx context.Context, gate, payload string) error {
		w := world(ctx).Host
		w.dc.resolveGate(gate, json.RawMessage(payload))
		w.signal()
		return w.settle()
	})

	sc.Step(`^the run waited for gate "([^"]*)" with schema "([^"]*)", acceptance condition "([^"]*)" and timeout (\S+)$`,
		func(ctx context.Context, gate, schema, accept, timeout string) error {
			w := world(ctx).Host
			if err := w.waitDone(); err != nil {
				return err
			}
			gates := w.gates()
			if len(gates) != 1 {
				return fmt.Errorf("gate waits %+v, want one", gates)
			}
			got := gates[0].gate
			duration, err := time.ParseDuration(timeout)
			if err != nil {
				return err
			}
			if got.Name != gate || rawText(got.Schema) != schema || got.AcceptIf != accept || got.Timeout != duration {
				return fmt.Errorf("gate wait %+v, want %s schema %s accept %s timeout %s", got, gate, schema, accept, timeout)
			}
			return nil
		})

	// FLOW-033: subflows.
	sc.Step(`^a flow whose node "([^"]*)" of kind "subflow" has the node spec (\{(?:[^{}]|\{[^{}]*\})*\})$`,
		func(ctx context.Context, node, spec string) error {
			w := world(ctx).Host
			w.newFlow("subflow", 1)
			w.addNode(node, "subflow", spec, "", "")
			w.addEdge(ir.Start, node, "")
			return nil
		})

	sc.Step(`^the child run "([^"]*)" returns (\{.*\})$`, func(ctx context.Context, name, result string) error {
		world(ctx).Host.dc.steps["child:"+name] = hostScript{result: json.RawMessage(result)}
		return nil
	})

	sc.Step(`^the child run "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, name, code string) error {
		world(ctx).Host.dc.steps["child:"+name] = hostScript{err: core.NewError(core.Code(code), "child failed", nil)}
		return nil
	})

	sc.Step(`^the run started the child run "([^"]*)" with input (\{.*\})$`, func(ctx context.Context, name, want string) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		for _, call := range w.dc.callLog() {
			if call.kind != "child" || call.child.Name != name {
				continue
			}
			if !jsonEquivalent(call.input, json.RawMessage(want)) {
				return fmt.Errorf("child input %s, want %s", call.input, want)
			}
			return nil
		}
		return fmt.Errorf("run did not start child %q", name)
	})

	// FLOW-035: failures.
	sc.Step(`^a flow whose node "([^"]*)" of kind "([^"]*)" has a registered node handler and precedes node "([^"]*)" of kind "([^"]*)" with node spec (\{.*\})$`,
		func(ctx context.Context, first, firstKind, second, secondKind, spec string) error {
			w := world(ctx).Host
			w.newFlow("failing", 1)
			w.addNode(first, firstKind, "", "", "")
			w.addNode(second, secondKind, spec, "", "")
			w.addEdge(ir.Start, first, "")
			w.addEdge(first, second, "")
			w.registerHandledKind(firstKind)
			w.behavior(first)
			return nil
		})

	sc.Step(`^the node handler of "([^"]*)" returns the error "([^"]*)"$`, func(ctx context.Context, node, code string) error {
		world(ctx).Host.handlerFails(node, core.NewError(core.Code(code), "handler failed", nil))
		return nil
	})

	sc.Step(`^the node handler of "([^"]*)" returns an unclassified error "([^"]*)"$`, func(ctx context.Context, node, message string) error {
		world(ctx).Host.handlerFails(node, errors.New(message))
		return nil
	})

	sc.Step(`^a flow whose node "([^"]*)" of kind "([^"]*)" has the node spec (\{.*\}) and precedes node "([^"]*)" of kind "([^"]*)" with node spec (\{.*\})$`,
		func(ctx context.Context, first, firstKind, firstSpec, second, secondKind, secondSpec string) error {
			w := world(ctx).Host
			w.newFlow("failing", 1)
			w.addNode(first, firstKind, firstSpec, "", "")
			w.addNode(second, secondKind, secondSpec, "", "")
			w.addEdge(ir.Start, first, "")
			w.addEdge(first, second, "")
			return nil
		})

	sc.Step(`^the run made no step$`, func(ctx context.Context) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		if steps := w.steps(); len(steps) > 0 {
			return fmt.Errorf("run made steps: %+v", steps)
		}
		return nil
	})

	sc.Step(`^(\d+) (second|seconds|minute|minutes|hour|hours) pass(?: without the gate "([^"]*)" being resolved)?$`,
		func(ctx context.Context, count int, unit, gate string) error {
			w := world(ctx).Host
			var duration time.Duration
			switch strings.TrimSuffix(unit, "s") {
			case "second":
				duration = time.Duration(count) * time.Second
			case "minute":
				duration = time.Duration(count) * time.Minute
			case "hour":
				duration = time.Duration(count) * time.Hour
			}
			w.dc.advance(duration)
			w.signal()
			return w.settle()
		})

	// FLOW-036: pure nodes.
	sc.Step(`^a flow routing node "([^"]*)" of kind "set" to node "([^"]*)" of kind "set" setting "([^"]*)" to "([^"]*)" when "([^"]*)", otherwise to node "([^"]*)" of kind "set" setting "([^"]*)" to "([^"]*)"$`,
		func(ctx context.Context, from, first, firstKey, firstValue, when, second, secondKey, secondValue string) error {
			w := world(ctx).Host
			w.newFlow("routing", 1)
			w.stateSchema(hostObjectSchema(map[string]string{
				"n":       `{"type":"integer"}`,
				firstKey:  `{"type":"string"}`,
				secondKey: `{"type":"string"}`,
			}))
			w.addNode(from, "set", "", "", "")
			w.addNode(first, "set", setSpec(firstKey, firstValue), "", "")
			w.addNode(second, "set", setSpec(secondKey, secondValue), "", "")
			w.addEdge(ir.Start, from, "")
			w.addEdge(from, first, when)
			w.addEdge(from, second, "")
			return nil
		})

	sc.Step(`^the run made no durable call after it completed$`, func(ctx context.Context) error {
		w := world(ctx).Host
		if err := w.waitDone(); err != nil {
			return err
		}
		// The host returns as soon as the run completes; the run goroutine has
		// ended, so nothing may follow.
		if calls := w.dc.callLog(); len(calls) != w.callsAtDone {
			return fmt.Errorf("run made %d durable calls after it completed", len(calls)-w.callsAtDone)
		}
		return nil
	})

	// FLOW-037: replay.
	sc.Step(`^a flow with node "([^"]*)" of kind "tool" with node spec (\{.*\}), then node "([^"]*)" of kind "wait" for "([^"]*)", then node "([^"]*)" of kind "agent" whose handler calls step "([^"]*)"$`,
		func(ctx context.Context, first, firstSpec, second, duration, third, step string) error {
			w := world(ctx).Host
			w.newFlow("replay", 1)
			w.addNode(first, "tool", firstSpec, "", "")
			w.addNode(second, "wait", fmt.Sprintf(`{"duration":%q}`, duration), "", "")
			w.addNode(third, "agent", "", "", "")
			w.addEdge(ir.Start, first, "")
			w.addEdge(first, second, "")
			w.addEdge(second, third, "")
			w.registerHandledKind("agent")
			w.behavior(third).step = step
			return nil
		})

	sc.Step(`^a first run recorded tool "([^"]*)" failing with code "([^"]*)" (\d+) times then returning (\{.*\}), the (\S+) timer of "([^"]*)" firing, and step "([^"]*)" returning (\{.*\})$`,
		func(ctx context.Context, tool, code string, attempts int, toolResult, duration, timerNode, step, stepResult string) error {
			w := world(ctx).Host
			w.dc.instant = true
			w.dc.steps[tool] = hostScript{result: json.RawMessage(toolResult)}
			w.dc.steps[step] = hostScript{result: json.RawMessage(stepResult)}
			// The retries belong to the runtime: the recorded history holds the
			// failed attempts and then the success the host's Step future returns.
			if err := w.start(`{}`); err != nil {
				return err
			}
			if err := w.waitDone(); err != nil {
				return err
			}
			if w.err != nil {
				return fmt.Errorf("first run failed: %w", w.err)
			}
			w.firstCalls = w.dc.callLog()
			_ = code
			_ = attempts
			_ = duration
			_ = timerNode
			return nil
		})

	sc.Step(`^the run is replayed from its recorded history$`, func(ctx context.Context) error {
		w := world(ctx).Host
		replay := w.dc.clone()
		w.dc = replay
		w.done = nil
		if err := w.start(`{}`); err != nil {
			return err
		}
		if err := w.waitDone(); err != nil {
			return err
		}
		if w.err != nil {
			return fmt.Errorf("replayed run failed: %w", w.err)
		}
		w.replayCalls = w.dc.callLog()
		return nil
	})

	sc.Step(`^the replayed run made the same durable calls in the same order as the first run$`, func(ctx context.Context) error {
		w := world(ctx).Host
		if got, want := callKinds(w.replayCalls), callKinds(w.firstCalls); strings.Join(got, ",") != strings.Join(want, ",") {
			return fmt.Errorf("replay calls %v, want %v", got, want)
		}
		return nil
	})

	sc.Step(`^the replayed run completed node "([^"]*)" with the result (\{.*\})$`, func(ctx context.Context, node, want string) error {
		w := world(ctx).Host
		b := w.behavior(node)
		if b == nil || !jsonEquivalent(b.result, json.RawMessage(want)) {
			return fmt.Errorf("replayed node %q result %s, want %s", node, b.result, want)
		}
		return nil
	})

	sc.Step(`^step "([^"]*)" was not re-executed during replay$`, func(ctx context.Context, step string) error {
		w := world(ctx).Host
		if count := w.dc.executeCount(step); count != 0 {
			return fmt.Errorf("step %q ran %d times in replay, want the recorded result", step, count)
		}
		return nil
	})

	// FLOW-038: a set node completes the run.
	sc.Step(`^a flow whose node "([^"]*)" of kind "set" sets "([^"]*)" to "([^"]*)"$`,
		func(ctx context.Context, node, key, value string) error {
			w := world(ctx).Host
			w.newFlow("set", 1)
			w.stateSchema(hostObjectSchema(map[string]string{key: `{"type":"string"}`}))
			w.addNode(node, "set", setSpec(key, value), "", "")
			w.addEdge(ir.Start, node, "")
			return nil
		})

	// FLOW-044: parallel timers.
	sc.Step(`^a flow that runs node "([^"]*)" of kind "wait" for "([^"]*)" and node "([^"]*)" of kind "wait" for "([^"]*)" in parallel$`,
		func(ctx context.Context, first, firstDuration, second, secondDuration string) error {
			w := world(ctx).Host
			w.newFlow("parallel", 1)
			w.addNode("fan", "parallel", "", "", "")
			w.addNode(first, "wait", fmt.Sprintf(`{"duration":%q}`, firstDuration), "", "")
			w.addNode(second, "wait", fmt.Sprintf(`{"duration":%q}`, secondDuration), "", "")
			w.addEdge(ir.Start, "fan", "")
			w.addEdge("fan", first, "")
			w.addEdge("fan", second, "")
			return nil
		})
}

func (w *hostWorld) steps() []hostCall {
	var out []hostCall
	for _, call := range w.dc.callLog() {
		if call.kind == "step" {
			out = append(out, call)
		}
	}
	return out
}

func (w *hostWorld) timers() []hostCall {
	var out []hostCall
	for _, call := range w.dc.callLog() {
		if call.kind == "timer" {
			out = append(out, call)
		}
	}
	return out
}

func (w *hostWorld) gates() []hostCall {
	var out []hostCall
	for _, call := range w.dc.callLog() {
		if call.kind == "gate" {
			out = append(out, call)
		}
	}
	return out
}

// nodeResult reads the result a node completed with: the state key it writes,
// the gate payload, the child run's result or a handler's output.
func (w *hostWorld) nodeResult(node string) (json.RawMessage, bool) {
	if w.flow != nil {
		if n, ok := w.flow.Nodes[node]; ok {
			if n.Out != "" {
				var state map[string]json.RawMessage
				if err := json.Unmarshal(w.result, &state); err == nil {
					if value, ok := state[n.Out]; ok {
						return value, true
					}
				}
			}
			if n.Kind == "subflow" {
				var spec struct {
					Flow string `json:"flow"`
				}
				if err := json.Unmarshal(n.Spec, &spec); err == nil {
					w.dc.mu.Lock()
					payload, ok := w.dc.childDone[spec.Flow]
					w.dc.mu.Unlock()
					if ok {
						return payload, true
					}
				}
			}
		}
	}
	w.dc.mu.Lock()
	gatePayload, hasGate := w.dc.gateDone[node]
	childPayload, hasChild := w.dc.childDone[node]
	w.dc.mu.Unlock()
	if hasGate {
		return gatePayload, true
	}
	if hasChild {
		return childPayload, true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if b := w.behaviors[node]; b != nil && len(b.result) > 0 {
		return b.result, true
	}
	if b := w.behaviors[node]; b != nil && len(b.output) > 0 && b.step == "" {
		return b.output, true
	}
	return nil, false
}

func hasEdgeFrom(def *flow.IR, from, to string) bool {
	for _, edge := range def.Edges {
		if edge.From == from && edge.To == to {
			return true
		}
	}
	return false
}

func callKinds(calls []hostCall) []string {
	out := make([]string, len(calls))
	for i, call := range calls {
		switch call.kind {
		case "step":
			out[i] = "step:" + call.name
		case "timer":
			out[i] = "timer:" + call.duration.String()
		case "gate":
			out[i] = "gate:" + call.gate.Name
		case "child":
			out[i] = "child:" + call.child.Name
		default:
			out[i] = call.kind
		}
	}
	return out
}

func rawText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	return string(raw)
}

func jsonEquivalent(left, right json.RawMessage) bool {
	var a, b any
	if err := decodeJSONValue(left, &a); err != nil {
		return false
	}
	if err := decodeJSONValue(right, &b); err != nil {
		return false
	}
	al, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bl, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(al, bl)
}

func decodeJSONValue(raw json.RawMessage, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(out)
}
