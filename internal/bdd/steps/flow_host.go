package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
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
	cond    *sync.Cond
	catalog *hostCatalog
	host    *host.Host
	dc      *hostFake

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

	firstJournal  []hostJournalEntry
	replayJournal []hostJournalEntry
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
			w.mu.Lock()
			released := b.released
			w.mu.Unlock()
			dc.mu.Lock()
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
		w.dc.markFinished()
		close(w.done)
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
	if w.dc != nil {
		w.dc.waitFinished()
	}
	select {
	case <-w.done:
		return nil
	default:
		return fmt.Errorf("run did not finish")
	}
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

// --- recording fake DurableContext ----------------------------------------

type hostCall struct {
	kind      string
	name      string
	input     json.RawMessage
	duration  time.Duration
	gate      runtime.GateSpec
	child     runtime.FlowRef
	stepOpts  runtime.StepOptions
	childOpts runtime.ChildOptions
}

type hostJournalEntry struct {
	kind      string
	name      string
	input     json.RawMessage
	duration  time.Duration
	gate      runtime.GateSpec
	child     runtime.FlowRef
	stepOpts  runtime.StepOptions
	childOpts runtime.ChildOptions
	idKind    string
	id        string
	result    json.RawMessage
	err       error
}

type hostScript struct {
	result    json.RawMessage
	err       error
	failTimes int
	failCode  core.Code
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
		f.dc.parked = true
		f.dc.cond.Broadcast()
		f.dc.cond.Wait()
		f.dc.parked = false
		f.dc.cond.Broadcast()
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
	mu           sync.Mutex
	cond         *sync.Cond
	now          time.Time
	calls        []hostCall
	journal      []hostJournalEntry
	journalIndex int
	steps        map[string]hostScript
	gates        map[string]*hostFuture
	timers       []hostTimer
	gateDone     map[string]json.RawMessage
	childDone    map[string]json.RawMessage
	emits        int
	checks       int
	instant      bool
	replaying    bool
	executed     map[string]int
	diverged     error
	parked       bool
	finished     bool
	nextID       int
}

func newHostFake() *hostFake {
	d := &hostFake{
		now:       time.Unix(1_700_000_000, 0).UTC(),
		steps:     map[string]hostScript{},
		gates:     map[string]*hostFuture{},
		gateDone:  map[string]json.RawMessage{},
		childDone: map[string]json.RawMessage{},
		executed:  map[string]int{},
	}
	d.cond = sync.NewCond(&d.mu)
	return d
}

func replayHostFake(journal []hostJournalEntry) *hostFake {
	d := newHostFake()
	d.replaying = true
	d.journal = append([]hostJournalEntry(nil), journal...)
	d.instant = true
	return d
}

func (d *hostFake) markFinished() {
	d.mu.Lock()
	d.finished = true
	d.parked = false
	d.cond.Broadcast()
	d.mu.Unlock()
}

func (d *hostFake) wake() {
	d.parked = false
	d.cond.Broadcast()
}

func (d *hostFake) abort() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.finished = true
	for _, f := range d.gates {
		if f != nil && !f.done {
			f.done = true
			f.err = core.NewError(core.CodeCancelled, "scenario ended", nil)
		}
	}
	for _, timer := range d.timers {
		timer.future.done = true
		if timer.gate {
			timer.future.err = core.NewError(core.CodeCancelled, "scenario ended", nil)
		}
	}
	d.timers = nil
	d.parked = false
	d.cond.Broadcast()
}

// record appends one durable call to the log. Caller must hold d.mu; it must not
// set parked (only blocking runtime calls do).
func (d *hostFake) record(c hostCall) {
	d.calls = append(d.calls, c)
	d.cond.Broadcast()
}

func (d *hostFake) appendJournal(e hostJournalEntry) {
	d.journal = append(d.journal, e)
}

func (d *hostFake) waitParkedOrFinished() {
	d.mu.Lock()
	for !d.parked && !d.finished {
		d.cond.Wait()
	}
	d.mu.Unlock()
}

func (d *hostFake) waitFinished() {
	d.mu.Lock()
	for !d.finished {
		d.cond.Wait()
	}
	d.mu.Unlock()
}

func (d *hostFake) copyJournal() []hostJournalEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]hostJournalEntry(nil), d.journal...)
}

func (d *hostFake) Context() context.Context { return context.Background() }
func (d *hostFake) Now() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.now
}

func (d *hostFake) NewID(kind string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.replaying {
		if d.journalIndex >= len(d.journal) {
			d.diverged = core.NewError(core.CodeDeterminismBroken, "replay: unexpected durable call", nil)
			return ""
		}
		e := d.journal[d.journalIndex]
		if e.kind != "id" || e.idKind != kind {
			d.diverged = core.NewError(core.CodeDeterminismBroken, "replay: id mismatch", nil)
			return ""
		}
		d.journalIndex++
		return e.id
	}
	d.nextID++
	id := fmt.Sprintf("%s-%d", kind, d.nextID)
	d.appendJournal(hostJournalEntry{kind: "id", idKind: kind, id: id})
	return id
}

func (d *hostFake) Rand(string) *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }

func (d *hostFake) journalOrFail(kind string) (hostJournalEntry, error) {
	if d.journalIndex >= len(d.journal) {
		d.diverged = core.NewError(core.CodeDeterminismBroken, "replay: unexpected durable call", map[string]any{"kind": kind})
		return hostJournalEntry{}, d.diverged
	}
	return d.journal[d.journalIndex], nil
}

func (d *hostFake) Step(name string, in any, o runtime.StepOptions) runtime.Future {
	raw := rawArgument(in)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.replaying {
		d.record(hostCall{kind: "step", name: name, input: raw, stepOpts: o})
		e, err := d.journalOrFail("step")
		if err != nil {
			return &hostFuture{dc: d, done: true, err: err}
		}
		if e.kind != "step" || e.name != name || !jsonEquivalent(e.input, raw) || e.stepOpts.IdempotencyKey != o.IdempotencyKey {
			d.diverged = core.NewError(core.CodeDeterminismBroken, "replay: step call mismatch", nil)
			return &hostFuture{dc: d, done: true, err: d.diverged}
		}
		d.journalIndex++
		return &hostFuture{dc: d, done: true, result: e.result, err: e.err}
	}
	script := d.steps[name]
	var result json.RawMessage
	var execErr error
	d.executed[name]++
	if script.failTimes > 0 {
		for i := 0; i < script.failTimes; i++ {
			d.executed[name]++
			if script.failCode != "" {
				_ = script.failCode
			}
		}
		result = append(json.RawMessage(nil), script.result...)
	} else {
		result = append(json.RawMessage(nil), script.result...)
		execErr = script.err
	}
	d.record(hostCall{kind: "step", name: name, input: raw, stepOpts: o})
	d.appendJournal(hostJournalEntry{
		kind: "step", name: name, input: append(json.RawMessage(nil), raw...),
		stepOpts: o, result: append(json.RawMessage(nil), result...), err: execErr,
	})
	return &hostFuture{dc: d, done: true, result: result, err: execErr}
}

func (d *hostFake) Child(ref runtime.FlowRef, in any, o runtime.ChildOptions) runtime.Future {
	raw := rawArgument(in)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.replaying {
		d.record(hostCall{kind: "child", child: ref, input: raw, childOpts: o})
		e, err := d.journalOrFail("child")
		if err != nil {
			return &hostFuture{dc: d, done: true, err: err}
		}
		if e.kind != "child" || e.child.Name != ref.Name || e.child.Hash != ref.Hash ||
			!jsonEquivalent(e.input, raw) || e.childOpts.ID != o.ID {
			d.diverged = core.NewError(core.CodeDeterminismBroken, "replay: child call mismatch", nil)
			return &hostFuture{dc: d, done: true, err: d.diverged}
		}
		d.journalIndex++
		return &hostFuture{dc: d, done: true, result: e.result, err: e.err}
	}
	script := d.steps["child:"+ref.Name]
	d.record(hostCall{kind: "child", child: ref, input: raw, childOpts: o})
	d.appendJournal(hostJournalEntry{
		kind: "child", child: ref, input: append(json.RawMessage(nil), raw...),
		childOpts: o, result: append(json.RawMessage(nil), script.result...), err: script.err,
	})
	if len(script.result) > 0 && script.err == nil {
		d.childDone[ref.Name] = script.result
	}
	d.executed["child:"+ref.Name]++
	return &hostFuture{dc: d, done: true, result: script.result, err: script.err}
}

func (d *hostFake) Sleep(duration time.Duration) runtime.Future {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.replaying {
		d.record(hostCall{kind: "timer", duration: duration})
		e, err := d.journalOrFail("timer")
		if err != nil {
			return &hostFuture{dc: d, done: true, err: err}
		}
		if e.kind != "timer" || e.duration != duration {
			d.diverged = core.NewError(core.CodeDeterminismBroken, "replay: timer mismatch", nil)
			return &hostFuture{dc: d, done: true, err: d.diverged}
		}
		d.journalIndex++
		return &hostFuture{dc: d, done: true}
	}
	f := &hostFuture{dc: d}
	if d.instant {
		f.done = true
		d.record(hostCall{kind: "timer", duration: duration})
		d.appendJournal(hostJournalEntry{kind: "timer", duration: duration})
		return f
	}
	d.timers = append(d.timers, hostTimer{deadline: d.now.Add(duration), future: f})
	d.record(hostCall{kind: "timer", duration: duration})
	d.appendJournal(hostJournalEntry{kind: "timer", duration: duration})
	return f
}

func (d *hostFake) Await(spec runtime.GateSpec) runtime.Future {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.replaying {
		d.record(hostCall{kind: "gate", gate: spec})
		e, err := d.journalOrFail("gate")
		if err != nil {
			return &hostFuture{dc: d, done: true, err: err}
		}
		if e.kind != "gate" || e.gate.Name != spec.Name || e.gate.Timeout != spec.Timeout ||
			e.gate.AcceptIf != spec.AcceptIf || !bytes.Equal(e.gate.Schema, spec.Schema) {
			d.diverged = core.NewError(core.CodeDeterminismBroken, "replay: gate mismatch", nil)
			return &hostFuture{dc: d, done: true, err: d.diverged}
		}
		d.journalIndex++
		return &hostFuture{dc: d, done: true, result: e.result, err: e.err}
	}
	f := &hostFuture{dc: d}
	d.gates[spec.Name] = f
	if spec.Timeout > 0 {
		d.timers = append(d.timers, hostTimer{deadline: d.now.Add(spec.Timeout), future: f, gate: true})
	}
	d.record(hostCall{kind: "gate", gate: spec})
	d.appendJournal(hostJournalEntry{kind: "gate", gate: spec})
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
		d.parked = true
		d.cond.Broadcast()
		d.cond.Wait()
		d.parked = false
		d.cond.Broadcast()
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
	d.wake()
}

func (d *hostFake) resolveGate(name string, payload json.RawMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.gates[name]
	if f != nil {
		f.done = true
		f.result = payload
		d.gateDone[name] = payload
		d.wake()
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

var replayFlowStep = regexp.MustCompile(`^a flow with node "([^"]*)" of kind "tool" with node spec (\{.*\}), then node "([^"]*)" of kind "wait" for "([^"]*)", then node "([^"]*)" of kind "agent" whose handler calls step "([^"]*)"$`)

// declareReplayFlow is the FLOW-037-01 flow declaration; flow_builder's catch-all routes matching text here.
func declareReplayFlow(ctx context.Context, first, firstSpec, second, duration, third, step string) error {
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
}

// --- step definitions ------------------------------------------------------

func registerFlowHost(sc *godog.ScenarioContext) {
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		world(ctx).Host.abortInFlightRun()
		return ctx, nil
	})

	sc.Step(`^a recording fake durable context$`, func(ctx context.Context) error {
		w := world(ctx).Host
		w.dc = newHostFake()
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

	sc.Step(`^a flow whose node "([^"]*)" of kind "set" precedes node "([^"]*)" of kind "agent" with a registered node handler$`,
		func(ctx context.Context, verify, repair string) error {
			w := world(ctx).Host
			def := w.newFlow("repair-loop", 1)
			def.State.Schema = json.RawMessage(`{"type":"object","properties":{"failed":{"type":"integer"}}}`)
			w.addNode(verify, "set", `{"verified":true}`, "", "")
			w.addNode(repair, "agent", `{"agent":"repair"}`, "", "failed")
			w.addEdge(ir.Start, verify, "")
			w.addEdge(verify, repair, "")
			w.registerHandledKind("agent")
			w.behavior(repair)
			return nil
		})

	sc.Step(`^node "([^"]*)" has a guard of (\d+) milli-points and routes back to node "([^"]*)"$`,
		func(ctx context.Context, node string, guard int, back string) error {
			w := world(ctx).Host
			def := w.ensureFlow()
			n := def.Nodes[node]
			n.Guard = &flow.Guard{CostMpts: int64(guard)}
			def.Nodes[node] = n
			w.addEdge(node, back, "")
			return nil
		})

	sc.Step(`^the node handler of "([^"]*)" returns (\{.*\}) with a settled cost of (\d+) milli-points$`,
		func(ctx context.Context, node, output string, cost int) error {
			b := world(ctx).Host.behavior(node)
			if err := json.Unmarshal([]byte(output), &b.output); err != nil {
				return fmt.Errorf("handler output %q: %w", output, err)
			}
			b.costMpts = int64(cost)
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

	sc.Step(`^(\d+) (second|seconds|minute|minutes|hour|hours) pass without the gate "[^"]*" being resolved$`,
		func(ctx context.Context, count int, unit string) error {
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

	sc.Step(`^a first run recorded tool "([^"]*)" failing with code "([^"]*)" (\d+) times then returning (\{.*\}), the (\S+) timer of "([^"]*)" firing, and step "([^"]*)" returning (\{.*\})$`,
		func(ctx context.Context, tool, code string, attempts int, toolResult, duration, timerNode, step, stepResult string) error {
			w := world(ctx).Host
			w.dc.instant = false
			w.dc.steps[tool] = hostScript{
				result:    json.RawMessage(toolResult),
				failTimes: attempts,
				failCode:  core.Code(code),
			}
			w.dc.steps[step] = hostScript{result: json.RawMessage(stepResult)}
			if err := w.start(`{}`); err != nil {
				return err
			}
			waitFor, err := time.ParseDuration(duration)
			if err != nil {
				return err
			}
			if timerNode == "b" {
				w.dc.advance(waitFor)
				if err := w.settle(); err != nil {
					return err
				}
			}
			if err := w.waitDone(); err != nil {
				return err
			}
			if w.err != nil {
				return fmt.Errorf("first run failed: %w", w.err)
			}
			if w.dc.executed[tool] != attempts+1 {
				return fmt.Errorf("tool %q ran %d times, want %d", tool, w.dc.executed[tool], attempts+1)
			}
			w.firstJournal = w.dc.copyJournal()
			_ = code
			return nil
		})

	sc.Step(`^the run is replayed from its recorded history$`, func(ctx context.Context) error {
		w := world(ctx).Host
		replay := replayHostFake(w.firstJournal)
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
		if w.dc.diverged != nil {
			return fmt.Errorf("replay diverged: %w", w.dc.diverged)
		}
		w.replayJournal = w.dc.copyJournal()
		return nil
	})

	sc.Step(`^the replayed run made the same durable calls in the same order as the first run$`, func(ctx context.Context) error {
		w := world(ctx).Host
		if err := journalsEqual(w.firstJournal, w.replayJournal); err != nil {
			return err
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
		if w.dc.diverged != nil {
			return fmt.Errorf("replay diverged: %w", w.dc.diverged)
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

func journalsEqual(first, replay []hostJournalEntry) error {
	if len(first) != len(replay) {
		return fmt.Errorf("journal length %d, want %d", len(replay), len(first))
	}
	for i := range first {
		a, b := first[i], replay[i]
		if a.kind != b.kind || a.name != b.name || a.duration != b.duration || a.id != b.id || a.idKind != b.idKind {
			return fmt.Errorf("entry %d: %+v, want %+v", i, b, a)
		}
		if a.child.Name != b.child.Name || a.child.Hash != b.child.Hash {
			return fmt.Errorf("entry %d child: %+v, want %+v", i, b.child, a.child)
		}
		if !rawJSONEqual(a.input, b.input) || !rawJSONEqual(a.result, b.result) {
			return fmt.Errorf("entry %d payload mismatch", i)
		}
		if a.stepOpts.IdempotencyKey != b.stepOpts.IdempotencyKey || a.childOpts.ID != b.childOpts.ID {
			return fmt.Errorf("entry %d keys mismatch", i)
		}
		if (a.err == nil) != (b.err == nil) {
			return fmt.Errorf("entry %d err mismatch", i)
		}
		if a.err != nil && core.CodeOf(a.err) != core.CodeOf(b.err) {
			return fmt.Errorf("entry %d code %v, want %v", i, core.CodeOf(b.err), core.CodeOf(a.err))
		}
	}
	return nil
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

func rawJSONEqual(left, right json.RawMessage) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	return jsonEquivalent(left, right)
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
