package host_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

const maxHandlerGuardRuns = 100

func checkHandlerRunLimit(node string, counts map[string]int) error {
	counts[node]++
	if counts[node] > maxHandlerGuardRuns {
		return core.NewError(core.CodeInternal,
			fmt.Sprintf("flow host test: node %q ran more than %d times: a guard did not stop the loop", node, maxHandlerGuardRuns),
			nil)
	}
	return nil
}

// --- a recording, scripted DurableContext ---------------------------------

type call struct {
	kind      string
	name      string
	input     json.RawMessage
	duration  time.Duration
	gate      runtime.GateSpec
	child     runtime.FlowRef
	stepOpts  runtime.StepOptions
	childOpts runtime.ChildOptions
}

type journalEntry struct {
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

type scripted struct {
	result    json.RawMessage
	err       error
	manual    bool
	failCode  core.Code
	failTimes int
}

type future struct {
	done   bool
	result json.RawMessage
	err    error
	dc     *fakeDC
}

func (f *future) Get(out any) error {
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

func (f *future) Ready() bool {
	f.dc.mu.Lock()
	defer f.dc.mu.Unlock()
	return f.done
}

type fakeDC struct {
	mu           sync.Mutex
	cond         *sync.Cond
	now          time.Time
	calls        []call
	journal      []journalEntry
	journalIndex int
	replaying    bool
	diverged     error
	parked       bool
	finished     bool
	steps        map[string]scripted
	gates        map[string]*future
	timers       []timedFuture
	manual       map[string]*future
	emits        []runtime.ProgressEvent
	checks       []runtime.SnapshotRef
	nextID       int
	instant      bool
	stepRun      map[string]int
	childRun     map[string]scripted
	// selectPrefer is the index Select returns when that future is ready; the default 0 is the first-ready behavior.
	selectPrefer int
}

type timedFuture struct {
	deadline time.Time
	future   *future
	gate     bool
}

func newFakeDC() *fakeDC {
	d := &fakeDC{
		now:      time.Unix(1_700_000_000, 0).UTC(),
		steps:    map[string]scripted{},
		gates:    map[string]*future{},
		manual:   map[string]*future{},
		stepRun:  map[string]int{},
		childRun: map[string]scripted{},
	}
	d.cond = sync.NewCond(&d.mu)
	return d
}

func replayFakeDC(journal []journalEntry) *fakeDC {
	d := newFakeDC()
	d.replaying = true
	d.journal = append([]journalEntry(nil), journal...)
	d.instant = true
	return d
}

func (d *fakeDC) wake() {
	d.parked = false
	d.cond.Broadcast()
}

// record appends one durable call to the log. Caller must hold d.mu; it must not
// set parked (only blocking runtime calls do).
func (d *fakeDC) record(c call) {
	d.calls = append(d.calls, c)
	d.cond.Broadcast()
}

type runHandle struct {
	dc     *fakeDC
	done   chan struct{}
	result json.RawMessage
	err    error
}

func (d *fakeDC) startRun(h *host.Host, ref runtime.FlowRef, input json.RawMessage) *runHandle {
	rh := &runHandle{dc: d, done: make(chan struct{})}
	go func() {
		rh.result, rh.err = h.Run(d, ref, input)
		d.mu.Lock()
		close(rh.done)
		d.finished = true
		d.parked = false
		d.cond.Broadcast()
		d.mu.Unlock()
	}()
	return rh
}

func (d *fakeDC) settle() {
	d.mu.Lock()
	for !d.parked && !d.finished {
		d.cond.Wait()
	}
	d.mu.Unlock()
}

func (rh *runHandle) finished() bool {
	rh.dc.mu.Lock()
	defer rh.dc.mu.Unlock()
	return rh.dc.finished
}

func (d *fakeDC) appendJournal(e journalEntry) {
	d.journal = append(d.journal, e)
}

func (d *fakeDC) journalOrFail(kind string) (journalEntry, error) {
	if d.journalIndex >= len(d.journal) {
		d.diverged = core.NewError(core.CodeDeterminismBroken, "replay: unexpected durable call", map[string]any{"kind": kind})
		return journalEntry{}, d.diverged
	}
	return d.journal[d.journalIndex], nil
}

func (d *fakeDC) matchStep(e journalEntry, name string, in json.RawMessage, o runtime.StepOptions) error {
	if e.kind != "step" || e.name != name || !jsonEqual(e.input, in) || e.stepOpts.IdempotencyKey != o.IdempotencyKey {
		d.diverged = core.NewError(core.CodeDeterminismBroken, "replay: step call mismatch", nil)
		return d.diverged
	}
	d.journalIndex++
	return nil
}

func (d *fakeDC) Context() context.Context { return context.Background() }
func (d *fakeDC) Now() time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.now
}

func (d *fakeDC) NewID(kind string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.replaying {
		e, err := d.journalOrFail("id")
		if err != nil {
			return ""
		}
		if e.kind != "id" || e.idKind != kind {
			d.diverged = core.NewError(core.CodeDeterminismBroken, "replay: id mismatch", nil)
			return ""
		}
		d.journalIndex++
		return e.id
	}
	d.nextID++
	id := fmt.Sprintf("%s-%d", kind, d.nextID)
	d.appendJournal(journalEntry{kind: "id", idKind: kind, id: id})
	return id
}

func (d *fakeDC) Rand(key string) *rand.Rand {
	return rand.New(rand.NewPCG(1, 2))
}

func (d *fakeDC) Step(name string, in any, o runtime.StepOptions) runtime.Future {
	raw := decodeArgument(in)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.replaying {
		d.record(call{kind: "step", name: name, input: raw, stepOpts: o})
		e, err := d.journalOrFail("step")
		if err != nil {
			return &future{dc: d, done: true, err: err}
		}
		if err := d.matchStep(e, name, raw, o); err != nil {
			return &future{dc: d, done: true, err: err}
		}
		return &future{dc: d, done: true, result: e.result, err: e.err}
	}
	script, ok := d.steps[name]
	if !ok {
		script = scripted{result: json.RawMessage(`{}`)}
	}
	var result json.RawMessage
	var execErr error
	if script.manual {
		f := &future{dc: d}
		d.manual[name] = f
		d.record(call{kind: "step", name: name, input: raw, stepOpts: o})
		d.appendJournal(journalEntry{kind: "step", name: name, input: append(json.RawMessage(nil), raw...), stepOpts: o})
		return f
	}
	d.stepRun[name]++
	result = append(json.RawMessage(nil), script.result...)
	if script.failTimes > 0 {
		for i := 0; i < script.failTimes; i++ {
			d.stepRun[name]++
		}
		execErr = nil
	} else {
		execErr = script.err
	}
	d.record(call{kind: "step", name: name, input: raw, stepOpts: o})
	d.appendJournal(journalEntry{
		kind: "step", name: name, input: append(json.RawMessage(nil), raw...),
		stepOpts: o, result: append(json.RawMessage(nil), result...), err: execErr,
	})
	return &future{dc: d, done: true, result: result, err: execErr}
}

func (d *fakeDC) Child(flowRef runtime.FlowRef, in any, o runtime.ChildOptions) runtime.Future {
	raw := decodeArgument(in)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.replaying {
		d.record(call{kind: "child", child: flowRef, input: raw, childOpts: o})
		e, err := d.journalOrFail("child")
		if err != nil {
			return &future{dc: d, done: true, err: err}
		}
		if e.kind != "child" || e.child.Name != flowRef.Name || e.child.Hash != flowRef.Hash ||
			!jsonEqual(e.input, raw) || e.childOpts.ID != o.ID {
			d.diverged = core.NewError(core.CodeDeterminismBroken, "replay: child call mismatch", nil)
			return &future{dc: d, done: true, err: d.diverged}
		}
		d.journalIndex++
		return &future{dc: d, done: true, result: e.result, err: e.err}
	}
	script := d.steps["child:"+flowRef.Name]
	d.record(call{kind: "child", child: flowRef, input: raw, childOpts: o})
	d.appendJournal(journalEntry{
		kind: "child", child: flowRef, input: append(json.RawMessage(nil), raw...),
		childOpts: o, result: append(json.RawMessage(nil), script.result...), err: script.err,
	})
	if !d.replaying {
		d.stepRun["child:"+flowRef.Name]++
	}
	return &future{dc: d, done: true, result: script.result, err: script.err}
}

func (d *fakeDC) Sleep(duration time.Duration) runtime.Future {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.replaying {
		d.record(call{kind: "timer", duration: duration})
		e, err := d.journalOrFail("timer")
		if err != nil {
			return &future{dc: d, done: true, err: err}
		}
		if e.kind != "timer" || e.duration != duration {
			d.diverged = core.NewError(core.CodeDeterminismBroken, "replay: timer mismatch", nil)
			return &future{dc: d, done: true, err: d.diverged}
		}
		d.journalIndex++
		return &future{dc: d, done: true}
	}
	f := &future{dc: d}
	if d.instant {
		f.done = true
		d.record(call{kind: "timer", duration: duration})
		d.appendJournal(journalEntry{kind: "timer", duration: duration})
		return f
	}
	d.timers = append(d.timers, timedFuture{deadline: d.now.Add(duration), future: f})
	d.record(call{kind: "timer", duration: duration})
	d.appendJournal(journalEntry{kind: "timer", duration: duration})
	return f
}

func (d *fakeDC) Await(spec runtime.GateSpec) runtime.Future {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.replaying {
		d.record(call{kind: "gate", gate: spec})
		e, err := d.journalOrFail("gate")
		if err != nil {
			return &future{dc: d, done: true, err: err}
		}
		if e.kind != "gate" || e.gate.Name != spec.Name || e.gate.Timeout != spec.Timeout ||
			e.gate.AcceptIf != spec.AcceptIf || !bytes.Equal(e.gate.Schema, spec.Schema) {
			d.diverged = core.NewError(core.CodeDeterminismBroken, "replay: gate mismatch", nil)
			return &future{dc: d, done: true, err: d.diverged}
		}
		d.journalIndex++
		f := &future{dc: d, done: e.err == nil && len(e.result) > 0, result: e.result, err: e.err}
		if !f.done {
			d.gates[spec.Name] = f
		}
		return f
	}
	f := &future{dc: d}
	d.gates[spec.Name] = f
	if spec.Timeout > 0 {
		d.timers = append(d.timers, timedFuture{deadline: d.now.Add(spec.Timeout), future: f, gate: true})
	}
	d.record(call{kind: "gate", gate: spec})
	d.appendJournal(journalEntry{kind: "gate", gate: spec})
	return f
}

func (d *fakeDC) Select(futures ...runtime.Future) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for {
		var ready []int
		for i, candidate := range futures {
			f := candidate.(*future)
			if f.done {
				ready = append(ready, i)
			}
		}
		if len(ready) > 0 {
			if d.selectPrefer >= 0 {
				for _, i := range ready {
					if i == d.selectPrefer {
						return i, nil
					}
				}
			}
			return ready[0], nil
		}
		d.parked = true
		d.cond.Broadcast()
		d.cond.Wait()
		d.parked = false
		d.cond.Broadcast()
	}
}

func (d *fakeDC) Emit(event runtime.ProgressEvent) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.emits = append(d.emits, event)
	return nil
}

func (d *fakeDC) Checkpoint(ref runtime.SnapshotRef) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.checks = append(d.checks, ref)
	return nil
}

func (d *fakeDC) Capabilities() runtime.Caps { return runtime.Caps{} }

func (d *fakeDC) advance(duration time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.now = d.now.Add(duration)
	kept := d.timers[:0]
	for _, timer := range d.timers {
		if timer.deadline.After(d.now) {
			kept = append(kept, timer)
			continue
		}
		if timer.gate {
			timer.future.done = true
			timer.future.err = core.NewError(core.CodeTimeout, "gate timed out", nil)
			d.patchTimerJournal(timer.future.err, nil)
		} else {
			timer.future.done = true
		}
	}
	d.timers = kept
	d.wake()
}

func (d *fakeDC) patchTimerJournal(err error, result json.RawMessage) {
	for i := len(d.journal) - 1; i >= 0; i-- {
		if d.journal[i].kind == "gate" {
			d.journal[i].err = err
			d.journal[i].result = result
			return
		}
	}
}

func (d *fakeDC) resolveGate(name string, payload json.RawMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.gates[name]
	f.done = true
	f.result = payload
	for i := len(d.journal) - 1; i >= 0; i-- {
		if d.journal[i].kind == "gate" && d.journal[i].gate.Name == name {
			d.journal[i].result = append(json.RawMessage(nil), payload...)
			break
		}
	}
	d.wake()
}

func (d *fakeDC) failManualSteps(errs map[string]error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for name, err := range errs {
		f := d.manual[name]
		if f == nil {
			continue
		}
		f.done = true
		f.err = err
	}
	d.wake()
}

func (d *fakeDC) resolveStep(name string, payload json.RawMessage, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.manual[name]
	f.done = true
	f.result = payload
	f.err = err
	for i := len(d.journal) - 1; i >= 0; i-- {
		if d.journal[i].kind == "step" && d.journal[i].name == name && d.journal[i].result == nil {
			d.journal[i].result = append(json.RawMessage(nil), payload...)
			d.journal[i].err = err
			break
		}
	}
	d.wake()
}

func (d *fakeDC) callLog() []call {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]call(nil), d.calls...)
}

func (d *fakeDC) stepExecutions() map[string]int {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]int{}
	for name, count := range d.stepRun {
		out[name] = count
	}
	return out
}

func (d *fakeDC) copyJournal() []journalEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]journalEntry(nil), d.journal...)
}

func decodeArgument(in any) json.RawMessage {
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

// --- a definition catalog --------------------------------------------------

type catalog struct {
	byHash map[string]*flow.IR
	latest map[string]*flow.IR
}

func newCatalog() *catalog {
	return &catalog{byHash: map[string]*flow.IR{}, latest: map[string]*flow.IR{}}
}

func (c *catalog) register(def *flow.IR) runtime.FlowRef {
	hash, err := ir.Hash(def)
	if err != nil {
		panic(err)
	}
	c.byHash[hash] = def
	c.latest[def.Name] = def
	return runtime.FlowRef{Name: def.Name, Hash: hash}
}

func (c *catalog) Load(_ context.Context, ref runtime.FlowRef) (*flow.IR, error) {
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

// --- flow builders ---------------------------------------------------------

func newFlow(name string) *flow.IR {
	return &flow.IR{
		Name:    name,
		Version: 1,
		State: flow.StateSpec{
			Schema: json.RawMessage(`{"type":"object","properties":{"v":{"type":"integer"},"worker":{"type":"object"},"out":{"type":"object"},"log":{"type":"array"}}}`),
		},
		Nodes: map[string]flow.Node{},
		Edges: []flow.Edge{},
	}
}

func addLinear(def *flow.IR, nodes ...string) {
	for i, id := range nodes {
		if i == 0 {
			def.Edges = append(def.Edges, flow.Edge{From: ir.Start, To: id})
			continue
		}
		def.Edges = append(def.Edges, flow.Edge{From: nodes[i-1], To: id})
	}
}

func node(kind, spec string, in, out string) flow.Node {
	n := flow.Node{Kind: kind, In: in, Out: out}
	if spec != "" {
		n.Spec = json.RawMessage(spec)
	}
	return n
}

// --- tests -----------------------------------------------------------------

func TestToolAndTaskRunAsOneStep(t *testing.T) {
	for _, kind := range []string{"tool", "task"} {
		t.Run(kind, func(t *testing.T) {
			def := newFlow("work-" + kind)
			def.Nodes["worker"] = node(kind, fmt.Sprintf(`{%q:"w1"}`, kind), "state.draft", "worker")
			addLinear(def, "worker")
			def.State.Schema = json.RawMessage(`{"type":"object","properties":{"draft":{"type":"object"},"worker":{"type":"object"}}}`)

			dc := newFakeDC()
			dc.steps["w1"] = scripted{result: json.RawMessage(`{"said":"done"}`)}
			cat := newCatalog()
			ref := cat.register(def)
			h := host.New(cat)

			got, err := h.Run(dc, ref, json.RawMessage(`{"draft":{"text":"hello"},"extra":1}`))
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			calls := dc.callLog()
			if len(calls) != 1 || calls[0].kind != "step" || calls[0].name != "w1" {
				t.Fatalf("calls %+v, want one step w1", calls)
			}
			assertJSON(t, calls[0].input, `{"text":"hello"}`)
			assertJSON(t, got, `{"draft":{"text":"hello"},"extra":1,"worker":{"said":"done"}}`)
		})
	}
}

func TestHandlerRunsThroughRegisteredHandler(t *testing.T) {
	def := newFlow("handled")
	def.Nodes["worker"] = node("agent", `{"agent":"w1"}`, "state.draft", "worker")
	addLinear(def, "worker")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"draft":{"type":"object"},"worker":{"type":"object"}}}`)

	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	var gotNode flow.Node
	var gotInput json.RawMessage
	h.RegisterHandler("agent", func(_ runtime.DurableContext, n flow.Node, input json.RawMessage) (host.NodeResult, error) {
		gotNode = n
		gotInput = append(json.RawMessage(nil), input...)
		return host.NodeResult{Output: json.RawMessage(`{"said":"done"}`)}, nil
	})

	out, err := h.Run(dc, ref, json.RawMessage(`{"draft":{"text":"hello"},"extra":1}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotNode.Name != "worker" || gotNode.Kind != "agent" || gotNode.Spec == nil {
		t.Fatalf("handler node %+v", gotNode)
	}
	assertJSON(t, gotInput, `{"text":"hello"}`)
	assertJSON(t, out, `{"draft":{"text":"hello"},"extra":1,"worker":{"said":"done"}}`)
	if calls := dc.callLog(); len(calls) != 0 {
		t.Fatalf("handler made durable calls: %+v", calls)
	}
}

func TestMissingHandlerFailsBeforeAnyNode(t *testing.T) {
	def := newFlow("triage")
	def.Nodes["prep"] = node("tool", `{"tool":"t1"}`, "", "")
	def.Nodes["worker"] = node("model", "", "", "")
	addLinear(def, "prep", "worker")

	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeUnsupported {
		t.Fatalf("code %v (%v), want unsupported", core.CodeOf(err), err)
	}
	if !bytes.Contains([]byte(err.Error()), []byte(`"worker"`)) {
		t.Fatalf("error %q does not name worker", err)
	}
	if calls := dc.callLog(); len(calls) != 0 {
		t.Fatalf("run made calls before failing: %+v", calls)
	}
}

func TestWaitRunsAsTimer(t *testing.T) {
	def := newFlow("cool")
	def.Nodes["cool_down"] = node("wait", `{"duration":"30s"}`, "", "")
	addLinear(def, "cool_down")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	rh := dc.startRun(h, ref, json.RawMessage(`{}`))
	waitFor(t, dc, func(d *fakeDC) bool { return len(d.calls) == 1 })
	if calls := dc.callLog(); calls[0].kind != "timer" || calls[0].duration != 30*time.Second {
		t.Fatalf("calls %+v, want one 30s timer", calls)
	}
	dc.advance(29 * time.Second)
	dc.settle()
	if rh.finished() {
		t.Fatalf("run finished after 29s: %v", rh.err)
	}
	dc.advance(1 * time.Second)
	dc.settle()
	<-rh.done
	if rh.err != nil {
		t.Fatalf("run after 30s: %v", rh.err)
	}
}

func TestGateWaitsAndCompletesWithPayload(t *testing.T) {
	def := newFlow("approve")
	def.Nodes["approve"] = node("gate", `{"schema":"review.json","accept_if":"input.approved","timeout":"72h"}`, "", "out")
	addLinear(def, "approve")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	rh := dc.startRun(h, ref, json.RawMessage(`{}`))
	waitFor(t, dc, func(d *fakeDC) bool { return len(d.calls) == 1 })
	spec := dc.callLog()[0].gate
	if spec.Name != "approve" || spec.AcceptIf != "input.approved" || spec.Timeout != 72*time.Hour {
		t.Fatalf("gate spec %+v", spec)
	}
	assertJSON(t, spec.Schema, `"review.json"`)
	dc.resolveGate("approve", json.RawMessage(`{"approved":true}`))
	dc.settle()
	<-rh.done
	if rh.err != nil {
		t.Fatalf("run: %v", rh.err)
	}
	assertJSON(t, rh.result, `{"out":{"approved":true}}`)
}

func TestGateTimeoutFailsRun(t *testing.T) {
	def := newFlow("approve")
	def.Nodes["approve"] = node("gate", `{"schema":"review.json","accept_if":"input.approved","timeout":"72h"}`, "", "")
	addLinear(def, "approve")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	rh := dc.startRun(h, ref, json.RawMessage(`{}`))
	waitFor(t, dc, func(d *fakeDC) bool { return len(d.calls) == 1 })
	dc.advance(72 * time.Hour)
	dc.settle()
	<-rh.done
	if core.CodeOf(rh.err) != core.CodeTimeout {
		t.Fatalf("code %v (%v), want timeout", core.CodeOf(rh.err), rh.err)
	}
}

func TestSubflowChildRunIsPinnedOnlyWhenTheSpecCarriesAHash(t *testing.T) {
	child := newFlow("checks")
	child.Nodes["done"] = node("set", `{"passed":4}`, "", "")
	addLinear(child, "done")
	cat := newCatalog()
	cat.register(child)

	tests := []struct {
		spec string
		want runtime.FlowRef
	}{
		{spec: `{"flow":"checks","hash":"pin-v1"}`, want: runtime.FlowRef{Name: "checks", Hash: "pin-v1"}},
		{spec: `{"flow":"checks"}`, want: runtime.FlowRef{Name: "checks", Hash: ""}},
	}
	for _, tc := range tests {
		t.Run(tc.spec, func(t *testing.T) {
			cat.byHash["pin-v1"] = child
			def := newFlow("verify")
			def.Nodes["verify"] = node("subflow", tc.spec, "", "out")
			addLinear(def, "verify")
			ref := cat.register(def)
			if ref.Hash == "" {
				t.Fatal("parent run must be pinned to a non-empty hash")
			}
			dc := newFakeDC()
			dc.steps["child:checks"] = scripted{result: json.RawMessage(`{"passed":4}`)}
			h := host.New(cat)
			if _, err := h.Run(dc, ref, json.RawMessage(`{}`)); err != nil {
				t.Fatalf("run: %v", err)
			}
			calls := dc.callLog()
			if len(calls) != 1 || calls[0].kind != "child" {
				t.Fatalf("calls %+v, want one child run", calls)
			}
			if calls[0].child != tc.want {
				t.Fatalf("child FlowRef %+v, want %+v", calls[0].child, tc.want)
			}
		})
	}
}

func TestSubflowRunsAsChild(t *testing.T) {
	def := newFlow("verify")
	def.Nodes["verify"] = node("subflow", `{"flow":"checks"}`, "", "out")
	addLinear(def, "verify")
	dc := newFakeDC()
	dc.steps["child:checks"] = scripted{result: json.RawMessage(`{"passed":4}`)}
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	out, err := h.Run(dc, ref, json.RawMessage(`{"tests":4}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	calls := dc.callLog()
	if len(calls) != 1 || calls[0].kind != "child" || calls[0].child.Name != "checks" {
		t.Fatalf("calls %+v, want one child run checks", calls)
	}
	assertJSON(t, calls[0].input, `{"tests":4}`)
	assertJSON(t, out, `{"tests":4,"out":{"passed":4}}`)
}

func TestParallelHandlerFailureStopsLaterHandlers(t *testing.T) {
	def := newFlow("parallel-handlers")
	def.Nodes["fan"] = flow.Node{Kind: "parallel"}
	def.Nodes["a"] = node("agent", `{"agent":"a"}`, "", "")
	def.Nodes["b"] = node("agent", `{"agent":"b"}`, "", "")
	def.Nodes["c"] = node("agent", `{"agent":"c"}`, "", "")
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "fan"},
		{From: "fan", To: "a"},
		{From: "fan", To: "b"},
		{From: "fan", To: "c"},
	}
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	var bCalls, cCalls int
	h.RegisterHandler("agent", func(_ runtime.DurableContext, n flow.Node, _ json.RawMessage) (host.NodeResult, error) {
		switch n.Name {
		case "a":
			return host.NodeResult{}, core.NewError(core.CodeVendor, "model failed", nil)
		case "b":
			bCalls++
			return host.NodeResult{Output: json.RawMessage(`{}`)}, nil
		case "c":
			cCalls++
			return host.NodeResult{Output: json.RawMessage(`{}`)}, nil
		}
		return host.NodeResult{}, nil
	})
	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeVendor {
		t.Fatalf("code %v (%v), want vendor_error", core.CodeOf(err), err)
	}
	if bCalls != 0 || cCalls != 0 {
		t.Fatalf("after a failed: b=%d c=%d, want 0", bCalls, cCalls)
	}
}

func TestLoadedDefinitionHashMismatchFailsBeforeAnyCall(t *testing.T) {
	cat := newCatalog()
	v1 := newFlow("review")
	v1.Nodes["prep"] = node("set", `{"v":1}`, "", "")
	addLinear(v1, "prep")
	ref := cat.register(v1)
	v2 := newFlow("review")
	v2.Nodes["prep"] = node("set", `{"v":2}`, "", "")
	addLinear(v2, "prep")
	cat.byHash[ref.Hash] = v2

	dc := newFakeDC()
	h := host.New(cat)
	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("run succeeded with mismatched pinned definition, want failure before any durable call")
	}
	if core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("code %v, want internal", core.CodeOf(err))
	}
	if calls := dc.callLog(); len(calls) != 0 {
		t.Fatalf("made calls before hash check: %+v", calls)
	}
}

func TestNegativeWaitDurationFailsRun(t *testing.T) {
	def := newFlow("bad-wait")
	def.Nodes["w"] = node("wait", `{"duration":"-1s"}`, "", "")
	addLinear(def, "w")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %v (%v), want invalid_argument", core.CodeOf(err), err)
	}
}

func TestNegativeGateTimeoutFailsRun(t *testing.T) {
	def := newFlow("bad-gate")
	def.Nodes["g"] = node("gate", `{"schema":"x.json","accept_if":"true","timeout":"-1s"}`, "", "")
	addLinear(def, "g")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %v (%v), want invalid_argument", core.CodeOf(err), err)
	}
}

func TestNodeFailurePayloadDoesNotLeakCauseOrDuplicateCode(t *testing.T) {
	marker := "SECRET_VENDOR_MARKER_XYZ"
	typed := &core.Error{
		Code:    core.CodeUnavailable,
		Message: "tool a failed",
		Cause:   errors.New(marker),
	}
	cases := []struct {
		name string
		run  func(cat *catalog, h *host.Host, dc *fakeDC, ref runtime.FlowRef) error
		code core.Code
		msg  string
	}{
		{
			name: "handler",
			code: core.CodeUnavailable,
			msg:  "tool a failed",
			run: func(cat *catalog, h *host.Host, dc *fakeDC, _ runtime.FlowRef) error {
				def := newFlow("handler-fail")
				def.Nodes["plan"] = node("agent", "", "", "")
				addLinear(def, "plan")
				ref := cat.register(def)
				h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
					return host.NodeResult{}, typed
				})
				_, err := h.Run(dc, ref, json.RawMessage(`{}`))
				return err
			},
		},
		{
			name: "step",
			code: core.CodeUnavailable,
			msg:  "tool a failed",
			run: func(cat *catalog, h *host.Host, dc *fakeDC, _ runtime.FlowRef) error {
				def := newFlow("step-fail")
				def.Nodes["worker"] = node("tool", `{"tool":"w1"}`, "", "")
				addLinear(def, "worker")
				ref := cat.register(def)
				dc.steps["w1"] = scripted{err: typed}
				_, err := h.Run(dc, ref, json.RawMessage(`{}`))
				return err
			},
		},
		{
			name: "gate",
			code: core.CodeTimeout,
			msg:  "gate timed out",
			run: func(cat *catalog, h *host.Host, dc *fakeDC, _ runtime.FlowRef) error {
				def := newFlow("gate-fail")
				def.Nodes["g"] = node("gate", `{"schema":"r.json","accept_if":"true","timeout":"1s"}`, "", "")
				addLinear(def, "g")
				ref := cat.register(def)
				rh := dc.startRun(h, ref, json.RawMessage(`{}`))
				waitFor(t, dc, func(d *fakeDC) bool { return len(d.calls) == 1 })
				dc.advance(time.Second)
				dc.settle()
				<-rh.done
				return rh.err
			},
		},
		{
			name: "child",
			code: core.CodeUnavailable,
			msg:  "tool a failed",
			run: func(cat *catalog, h *host.Host, dc *fakeDC, _ runtime.FlowRef) error {
				def := newFlow("child-fail")
				def.Nodes["verify"] = node("subflow", `{"flow":"checks"}`, "", "")
				addLinear(def, "verify")
				ref := cat.register(def)
				dc.steps["child:checks"] = scripted{err: typed}
				_, err := h.Run(dc, ref, json.RawMessage(`{}`))
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dc := newFakeDC()
			cat := newCatalog()
			h := host.New(cat)
			err := tc.run(cat, h, dc, runtime.FlowRef{})
			if core.CodeOf(err) != tc.code {
				t.Fatalf("code %v (%v), want %s", core.CodeOf(err), err, tc.code)
			}
			var pe *core.Error
			if !errors.As(err, &pe) || pe.Message != tc.msg {
				t.Fatalf("message %q, want %q (%v)", pe.Message, tc.msg, err)
			}
			if strings.Contains(err.Error(), marker) {
				t.Fatalf("run error leaks marker: %s", err.Error())
			}
		})
	}
}

func TestUnclassifiedErrorsUseInternalMessageWithoutRawText(t *testing.T) {
	secret := "secret vendor text"
	def := newFlow("unclassified")
	def.Nodes["plan"] = node("agent", "", "", "")
	addLinear(def, "plan")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
		return host.NodeResult{}, errors.New(secret)
	})
	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("code %v, want internal", core.CodeOf(err))
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("leaked secret in %q", err.Error())
	}
}

func TestIdempotencyKeysDifferAcrossVisitsAndMatchOnReplay(t *testing.T) {
	def := newFlow("loop")
	def.Nodes["work"] = flow.Node{
		Kind:  "tool",
		Spec:  json.RawMessage(`{"tool":"w1"}`),
		Guard: &flow.Guard{MaxVisits: 2, While: `true`},
	}
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "work"},
		{From: "work", To: "work"},
	}
	dc := newFakeDC()
	dc.steps["w1"] = scripted{result: json.RawMessage(`{"ok":true}`)}
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	if _, err := h.Run(dc, ref, json.RawMessage(`{}`)); core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("first run: %v", err)
	}
	journal := dc.copyJournal()
	var keys []string
	for _, e := range journal {
		if e.kind == "step" {
			keys = append(keys, e.stepOpts.IdempotencyKey)
		}
	}
	if len(keys) != 2 || keys[0] == keys[1] {
		t.Fatalf("visit keys %+v, want two distinct keys", keys)
	}
	want0 := fmt.Sprintf("loop@%s/work#0@1", ref.Hash)
	want1 := fmt.Sprintf("loop@%s/work#0@2", ref.Hash)
	if keys[0] != want0 || keys[1] != want1 {
		t.Fatalf("keys %+v, want %q and %q", keys, want0, want1)
	}
	replay := replayFakeDC(journal)
	if _, err := h.Run(replay, ref, json.RawMessage(`{}`)); core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("replay: %v", err)
	}
	var replayKeys []string
	for _, c := range replay.callLog() {
		if c.kind == "step" {
			replayKeys = append(replayKeys, c.stepOpts.IdempotencyKey)
		}
	}
	if fmt.Sprint(replayKeys) != fmt.Sprint(keys) {
		t.Fatalf("replay keys %v, want %v", replayKeys, keys)
	}
}

func TestMapFanOutUsesDistinctIdempotencyKeys(t *testing.T) {
	def := newFlow("mapped")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"items":{"type":"array"}}}`)
	def.Nodes["fan"] = flow.Node{Kind: "map", Spec: json.RawMessage(`{"over":"state.items","node":"work"}`)}
	def.Nodes["work"] = node("tool", `{"tool":"w1"}`, "", "")
	def.Edges = []flow.Edge{{From: ir.Start, To: "fan"}, {From: "fan", To: "work"}}
	dc := newFakeDC()
	dc.steps["w1"] = scripted{result: json.RawMessage(`"x"`)}
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	if _, err := h.Run(dc, ref, json.RawMessage(`{"items":[1,2,3]}`)); err != nil {
		t.Fatalf("run: %v", err)
	}
	seen := map[string]bool{}
	for _, c := range dc.callLog() {
		if c.kind != "step" {
			continue
		}
		k := c.stepOpts.IdempotencyKey
		if seen[k] {
			t.Fatalf("duplicate idempotency key %q", k)
		}
		seen[k] = true
	}
	if len(seen) != 3 {
		t.Fatalf("got %d keys, want 3", len(seen))
	}
	wantKeys := []string{
		fmt.Sprintf("mapped@%s/work#1@1", ref.Hash),
		fmt.Sprintf("mapped@%s/work#2@1", ref.Hash),
		fmt.Sprintf("mapped@%s/work#3@1", ref.Hash),
	}
	got := make([]string, 0, 3)
	for _, c := range dc.callLog() {
		if c.kind == "step" {
			got = append(got, c.stepOpts.IdempotencyKey)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(wantKeys) {
		t.Fatalf("keys %v, want %v", got, wantKeys)
	}
}

func TestIdempotencyKeyUsesDefinitionNameNotCallerRefName(t *testing.T) {
	def := newFlow("real")
	def.Nodes["work"] = node("tool", `{"tool":"w1"}`, "", "")
	addLinear(def, "work")
	dc := newFakeDC()
	dc.steps["w1"] = scripted{result: json.RawMessage(`{}`)}
	cat := newCatalog()
	ref := cat.register(def)
	alias := runtime.FlowRef{Name: "typo", Hash: ref.Hash}
	h := host.New(cat)
	if _, err := h.Run(dc, alias, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("run: %v", err)
	}
	wantPrefix := "real@" + ref.Hash + "/"
	for _, c := range dc.callLog() {
		if c.kind != "step" {
			continue
		}
		k := c.stepOpts.IdempotencyKey
		if !strings.HasPrefix(k, wantPrefix) {
			t.Fatalf("key %q, want prefix %q", k, wantPrefix)
		}
		if strings.HasPrefix(k, "typo@") {
			t.Fatalf("key %q uses caller ref name", k)
		}
	}
}

func TestIdempotencyKeyFormatPinned(t *testing.T) {
	def := newFlow("fmt")
	back := node("tool", `{"tool":"w1"}`, "", "")
	back.Guard = &flow.Guard{MaxVisits: 2, While: `true`}
	def.Nodes["first"] = node("tool", `{"tool":"w1"}`, "", "")
	def.Nodes["back"] = back
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "first"},
		{From: "first", To: "back"},
		{From: "back", To: "back"},
	}
	dc := newFakeDC()
	dc.steps["w1"] = scripted{result: json.RawMessage(`{}`)}
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	if _, err := h.Run(dc, ref, json.RawMessage(`{}`)); core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("run: %v", err)
	}
	var keys []string
	for _, c := range dc.callLog() {
		if c.kind == "step" {
			keys = append(keys, c.stepOpts.IdempotencyKey)
		}
	}
	if len(keys) != 3 {
		t.Fatalf("step keys %+v, want 3", keys)
	}
	want := []string{
		fmt.Sprintf("fmt@%s/first#0@1", ref.Hash),
		fmt.Sprintf("fmt@%s/back#0@2", ref.Hash),
		fmt.Sprintf("fmt@%s/back#0@3", ref.Hash),
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("key[%d] %q, want %q", i, keys[i], want[i])
		}
	}
}

func TestSuperstepFailureUsesFirstSelectResult(t *testing.T) {
	build := func() (*fakeDC, *host.Host, runtime.FlowRef) {
		def := newFlow("pair")
		def.Nodes["fan"] = flow.Node{Kind: "parallel"}
		def.Nodes["left"] = node("tool", `{"tool":"left"}`, "", "")
		def.Nodes["right"] = node("tool", `{"tool":"right"}`, "", "")
		def.Edges = []flow.Edge{
			{From: ir.Start, To: "fan"},
			{From: "fan", To: "left"},
			{From: "fan", To: "right"},
		}
		dc := newFakeDC()
		dc.steps["left"] = scripted{manual: true}
		dc.steps["right"] = scripted{manual: true}
		cat := newCatalog()
		ref := cat.register(def)
		h := host.New(cat)
		return dc, h, ref
	}
	runWithOrder := func(t *testing.T, prefer int, wantCode core.Code) {
		t.Helper()
		dc, h, ref := build()
		dc.selectPrefer = prefer
		rh := dc.startRun(h, ref, json.RawMessage(`{}`))
		waitFor(t, dc, func(d *fakeDC) bool { return len(d.manual) == 2 })
		dc.failManualSteps(map[string]error{
			"left":  core.NewError(core.CodeUnavailable, "left failed", nil),
			"right": core.NewError(core.CodeTimeout, "right failed", nil),
		})
		dc.settle()
		<-rh.done
		if core.CodeOf(rh.err) != wantCode {
			t.Fatalf("select prefer %d: code %v (%v), want %s", prefer, core.CodeOf(rh.err), rh.err, wantCode)
		}
	}
	t.Run("prefers_index_zero", func(t *testing.T) {
		runWithOrder(t, 0, core.CodeUnavailable)
	})
	t.Run("prefers_index_one", func(t *testing.T) {
		runWithOrder(t, 1, core.CodeTimeout)
	})
}

func TestHandlerFailureFailsRunAndStopsLaterNodes(t *testing.T) {
	def := newFlow("handled")
	def.Nodes["plan"] = node("agent", `{"agent":"p"}`, "", "")
	def.Nodes["review"] = node("tool", `{"tool":"t1"}`, "", "")
	addLinear(def, "plan", "review")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
		return host.NodeResult{}, core.NewError(core.CodeVendor, "model vendor failed", nil)
	})

	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeVendor {
		t.Fatalf("code %v (%v), want vendor_error", core.CodeOf(err), err)
	}
	if calls := dc.callLog(); len(calls) != 0 {
		t.Fatalf("run made calls after failing handler: %+v", calls)
	}
}

func TestUnclassifiedHandlerFailureIsInternal(t *testing.T) {
	def := newFlow("handled")
	def.Nodes["plan"] = node("agent", "", "", "")
	addLinear(def, "plan")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
		return host.NodeResult{}, errors.New("unexpected panic")
	})
	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("code %v (%v), want internal", core.CodeOf(err), err)
	}
}

func TestStepFailureFailsRunWithItsCode(t *testing.T) {
	for _, kind := range []string{"tool", "task"} {
		t.Run(kind, func(t *testing.T) {
			def := newFlow("work-" + kind)
			def.Nodes["worker"] = node(kind, fmt.Sprintf(`{%q:"w1"}`, kind), "", "")
			def.Nodes["after"] = node("tool", `{"tool":"t2"}`, "", "")
			addLinear(def, "worker", "after")
			dc := newFakeDC()
			dc.steps["w1"] = scripted{err: core.NewError(core.CodeUnavailable, "step down", nil)}
			cat := newCatalog()
			ref := cat.register(def)
			h := host.New(cat)

			_, err := h.Run(dc, ref, json.RawMessage(`{}`))
			if core.CodeOf(err) != core.CodeUnavailable {
				t.Fatalf("code %v (%v), want unavailable", core.CodeOf(err), err)
			}
			calls := dc.callLog()
			if len(calls) != 1 || calls[0].name != "w1" {
				t.Fatalf("calls %+v, want exactly one step w1", calls)
			}
			assertJSON(t, calls[0].input, `{}`)
		})
	}
}

func TestChildFailureFailsRunWithItsCode(t *testing.T) {
	def := newFlow("verify")
	def.Nodes["verify"] = node("subflow", `{"flow":"checks"}`, "", "")
	def.Nodes["after"] = node("tool", `{"tool":"t2"}`, "", "")
	addLinear(def, "verify", "after")
	dc := newFakeDC()
	dc.steps["child:checks"] = scripted{err: core.NewError(core.CodeUnavailable, "child down", nil)}
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("code %v (%v), want unavailable", core.CodeOf(err), err)
	}
	calls := dc.callLog()
	if len(calls) != 1 || calls[0].kind != "child" {
		t.Fatalf("calls %+v, want only the child run", calls)
	}
}

func TestPureSetAndRoutingMakeNoDurableCall(t *testing.T) {
	def := newFlow("pure")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"},"route":{"type":"string"}}}`)
	def.Nodes["prep"] = node("set", `{}`, "", "")
	def.Nodes["fast"] = node("set", `{"route":"fast"}`, "", "")
	def.Nodes["slow"] = node("set", `{"route":"slow"}`, "", "")
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "prep"},
		{From: "prep", To: "fast", When: "state.n < 3"},
		{From: "prep", To: "slow"},
	}
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	out, err := h.Run(dc, ref, json.RawMessage(`{"n":5}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertJSON(t, out, `{"n":5,"route":"slow"}`)
	if calls := dc.callLog(); len(calls) != 0 {
		t.Fatalf("pure flow made durable calls: %+v", calls)
	}
}

func TestCompletedRunMakesNoFurtherCall(t *testing.T) {
	def := newFlow("greet")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"greeting":{"type":"string"}}}`)
	def.Nodes["prep"] = node("set", `{"greeting":"hi"}`, "", "")
	addLinear(def, "prep")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	out, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertJSON(t, out, `{"greeting":"hi"}`)
	if calls := dc.callLog(); len(calls) != 0 {
		t.Fatalf("completed run made durable calls: %+v", calls)
	}
}

func TestSuperstepStartsEveryCallBeforeWaiting(t *testing.T) {
	def := newFlow("fanout")
	def.Nodes["fan"] = flow.Node{Kind: "parallel"}
	def.Nodes["a"] = node("wait", `{"duration":"10s"}`, "", "")
	def.Nodes["b"] = node("wait", `{"duration":"30s"}`, "", "")
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "fan"},
		{From: "fan", To: "b"},
		{From: "fan", To: "a"},
	}
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	rh := dc.startRun(h, ref, json.RawMessage(`{}`))
	waitFor(t, dc, func(d *fakeDC) bool { return len(d.calls) == 2 })
	calls := dc.callLog()
	if calls[0].duration != 10*time.Second || calls[1].duration != 30*time.Second {
		t.Fatalf("timers %+v, want 10s then 30s in node-id order", calls)
	}
	dc.advance(10 * time.Second)
	dc.settle()
	if rh.finished() {
		t.Fatalf("run finished with a timer pending: %v", rh.err)
	}
	dc.advance(20 * time.Second)
	dc.settle()
	<-rh.done
	if rh.err != nil {
		t.Fatalf("run: %v", rh.err)
	}
}

func TestReplayMakesTheSameCallsWithoutExecutingHandlerStepsAgain(t *testing.T) {
	def := newFlow("replay")
	def.Nodes["a"] = node("tool", `{"tool":"a"}`, "", "")
	def.Nodes["b"] = node("wait", `{"duration":"10s"}`, "", "")
	def.Nodes["c"] = node("agent", `{"agent":"c"}`, "", "")
	addLinear(def, "a", "b", "c")

	first := newFakeDC()
	first.instant = true
	first.steps["a"] = scripted{failTimes: 4, failCode: core.CodeUnavailable, result: json.RawMessage(`{"t":1}`)}
	first.steps["c_step"] = scripted{result: json.RawMessage(`{"ok":true}`)}

	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	h.RegisterHandler("agent", func(dc runtime.DurableContext, _ flow.Node, input json.RawMessage) (host.NodeResult, error) {
		var out json.RawMessage
		if err := dc.Step("c_step", input, runtime.StepOptions{}).Get(&out); err != nil {
			return host.NodeResult{}, err
		}
		return host.NodeResult{Output: out}, nil
	})

	if _, err := h.Run(first, ref, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	journal := first.copyJournal()
	replay := replayFakeDC(journal)
	if _, err := h.Run(replay, ref, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.diverged != nil {
		t.Fatalf("replay diverged: %v", replay.diverged)
	}
	if got, want := kinds(replay.callLog()), kinds(first.callLog()); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("replay calls %v, want %v", got, want)
	}
	if count := replay.stepExecutions()["c_step"]; count != 0 {
		t.Fatalf("c_step ran %d times in replay, want it taken from history", count)
	}
	if first.stepRun["a"] != 5 {
		t.Fatalf("tool a ran %d times in first run, want 5 (4 fails + success)", first.stepRun["a"])
	}
}

func TestReplayCoversChildGateAndMap(t *testing.T) {
	def := newFlow("replay-mix")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"items":{"type":"array"},"childOut":{"type":"object"}}}`)
	def.Nodes["sub"] = node("subflow", `{"flow":"child"}`, "", "childOut")
	def.Nodes["hold"] = node("gate", `{"schema":"r.json","accept_if":"true","timeout":"1h"}`, "", "")
	def.Nodes["pause"] = node("wait", `{"duration":"1s"}`, "", "")
	def.Nodes["fan"] = flow.Node{Kind: "map", Spec: json.RawMessage(`{"over":"state.items","node":"work"}`)}
	def.Nodes["work"] = node("tool", `{"tool":"w1"}`, "", "")
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "sub"},
		{From: "sub", To: "hold"},
		{From: "hold", To: "pause"},
		{From: "pause", To: "fan"},
		{From: "fan", To: "work"},
	}
	dc := newFakeDC()
	dc.steps["child:child"] = scripted{result: json.RawMessage(`{"child":1}`)}
	dc.steps["w1"] = scripted{result: json.RawMessage(`"x"`)}
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	rh := dc.startRun(h, ref, json.RawMessage(`{"items":["a","b"]}`))
	waitFor(t, dc, func(d *fakeDC) bool {
		for _, c := range d.calls {
			if c.kind == "gate" {
				return true
			}
		}
		return false
	})
	dc.resolveGate("hold", json.RawMessage(`{"ok":true}`))
	dc.settle()
	dc.advance(time.Second)
	dc.settle()
	<-rh.done
	if rh.err != nil {
		t.Fatalf("first run: %v", rh.err)
	}
	wantFP := callFingerprints(dc.callLog())
	for _, kind := range []string{"child", "gate", "timer", "step"} {
		if !slicesContainsKind(wantFP, kind) {
			t.Fatalf("first run missing %s, fingerprints %v", kind, wantFP)
		}
	}
	journal := dc.copyJournal()
	idCount := 0
	for _, e := range journal {
		if e.kind == "id" {
			idCount++
		}
	}
	if idCount < 1 {
		t.Fatalf("journal has %d id entries, want at least one for subflow NewID", idCount)
	}
	t.Run("replay matches journal", func(t *testing.T) {
		replay := replayFakeDC(journal)
		if _, err := h.Run(replay, ref, json.RawMessage(`{"items":["a","b"]}`)); err != nil {
			t.Fatalf("replay: %v", err)
		}
		if replay.diverged != nil {
			t.Fatalf("diverged: %v", replay.diverged)
		}
		got := callFingerprints(replay.callLog())
		if fmt.Sprint(got) != fmt.Sprint(wantFP) {
			t.Fatalf("replay fingerprints %v, want %v", got, wantFP)
		}
	})
	t.Run("gate spec mismatch diverges", func(t *testing.T) {
		bad := append([]journalEntry(nil), journal...)
		for i := range bad {
			if bad[i].kind == "gate" {
				bad[i].gate.Timeout += time.Second
				break
			}
		}
		replay := replayFakeDC(bad)
		_, err := h.Run(replay, ref, json.RawMessage(`{"items":["a","b"]}`))
		if core.CodeOf(err) != core.CodeDeterminismBroken && replay.diverged == nil {
			t.Fatalf("want determinism broken, got %v diverged=%v", err, replay.diverged)
		}
	})
	t.Run("child id mismatch diverges", func(t *testing.T) {
		bad := append([]journalEntry(nil), journal...)
		for i := range bad {
			if bad[i].kind == "child" {
				bad[i].childOpts.ID = "changed"
				break
			}
		}
		replay := replayFakeDC(bad)
		_, err := h.Run(replay, ref, json.RawMessage(`{"items":["a","b"]}`))
		if core.CodeOf(err) != core.CodeDeterminismBroken && replay.diverged == nil {
			t.Fatalf("want determinism broken, got %v diverged=%v", err, replay.diverged)
		}
	})
}

func TestReplayDetectsStepNameMismatch(t *testing.T) {
	def := newFlow("replay-div")
	def.Nodes["a"] = node("tool", `{"tool":"a"}`, "", "")
	addLinear(def, "a")
	first := newFakeDC()
	first.steps["a"] = scripted{result: json.RawMessage(`{}`)}
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	if _, err := h.Run(first, ref, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("first: %v", err)
	}
	journal := first.copyJournal()
	journal[0].name = "b"
	replay := replayFakeDC(journal)
	_, err := h.Run(replay, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeDeterminismBroken && replay.diverged == nil {
		t.Fatalf("want determinism broken, got %v diverged=%v", err, replay.diverged)
	}
}

func TestPinnedDefinitionSurvivesANewVersion(t *testing.T) {
	cat := newCatalog()
	v1 := newFlow("review")
	v1.Version = 1
	v1.Nodes["prep"] = node("set", `{"v":1}`, "", "")
	addLinear(v1, "prep")
	ref := cat.register(v1)

	v2 := newFlow("review")
	v2.Version = 2
	v2.Nodes["prep"] = node("set", `{"v":2}`, "", "")
	addLinear(v2, "prep")
	cat.register(v2)

	dc := newFakeDC()
	h := host.New(cat)
	out, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	assertJSON(t, out, `{"v":1}`)
}

// TestCompletionOrderDoesNotChangeTheResult drives a parallel fan-out with
// random resolution orders. The host starts the calls in command order and the
// machine merges their updates canonically, so the final state is the same.
func TestCompletionOrderDoesNotChangeTheResult(t *testing.T) {
	def := newFlow("order")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"log":{"type":"array"}}}`)
	def.State.Reducers = map[string]string{"log": "append"}
	def.Nodes["fan"] = flow.Node{Kind: "parallel"}
	for _, id := range []string{"a", "b", "c"} {
		def.Nodes[id] = node("tool", fmt.Sprintf(`{"tool":%q}`, id), "", "log")
		def.Edges = append(def.Edges, flow.Edge{From: "fan", To: id})
	}
	def.Edges = append([]flow.Edge{{From: ir.Start, To: "fan"}}, def.Edges...)
	cat := newCatalog()
	ref := cat.register(def)

	var want json.RawMessage
	rng := rand.New(rand.NewPCG(7, 11))
	for round := 0; round < 20; round++ {
		dc := newFakeDC()
		for _, id := range []string{"a", "b", "c"} {
			dc.steps[id] = scripted{manual: true}
		}
		h := host.New(cat)
		rh := dc.startRun(h, ref, json.RawMessage(`{}`))
		waitFor(t, dc, func(d *fakeDC) bool { return len(d.manual) == 3 })
		order := []string{"a", "b", "c"}
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, id := range order {
			payload, _ := json.Marshal(id)
			dc.resolveStep(id, payload, nil)
			dc.settle()
		}
		<-rh.done
		if rh.err != nil {
			t.Fatalf("round %d: %v", round, rh.err)
		}
		if want == nil {
			want = rh.result
			continue
		}
		if !jsonEqual(want, rh.result) {
			t.Fatalf("round %d: state %s, want %s", round, rh.result, want)
		}
	}
}

func TestWithMaxFanoutLimitsMapAndRejectsZero(t *testing.T) {
	makeFlow := func(n int) *flow.IR {
		items := make([]int, n)
		for i := range items {
			items[i] = i
		}
		payload, _ := json.Marshal(map[string]any{"items": items})
		def := newFlow("big-map")
		def.State.Schema = json.RawMessage(`{"type":"object","properties":{"items":{"type":"array"}}}`)
		def.Nodes["fan"] = flow.Node{Kind: "set", Spec: payload}
		def.Nodes["mapn"] = flow.Node{Kind: "map", Spec: json.RawMessage(`{"over":"state.items","node":"work"}`)}
		def.Nodes["work"] = node("tool", `{"tool":"w1"}`, "", "")
		def.Edges = []flow.Edge{
			{From: ir.Start, To: "fan"},
			{From: "fan", To: "mapn"},
			{From: "mapn", To: "work"},
		}
		return def
	}
	cat := newCatalog()
	ref3 := cat.register(makeFlow(3))
	ref4 := cat.register(makeFlow(4))
	dc := newFakeDC()
	dc.steps["w1"] = scripted{result: json.RawMessage(`"x"`)}
	h := host.New(cat, host.WithMaxFanout(3))
	if _, err := h.Run(dc, ref3, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("map of 3: %v", err)
	}
	if len(dc.callLog()) != 3 {
		t.Fatalf("map of 3: %d durable calls, want 3", len(dc.callLog()))
	}
	dc2 := newFakeDC()
	_, err := h.Run(dc2, ref4, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("map of 4: %v, want budget_exceeded", err)
	}
	if len(dc2.callLog()) != 0 {
		t.Fatalf("map of 4 made durable calls: %+v", dc2.callLog())
	}
	h0 := host.New(cat, host.WithMaxFanout(0))
	if _, err := h0.Run(newFakeDC(), ref3, json.RawMessage(`{}`)); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("max fanout 0: %v, want invalid_argument", err)
	}
}

// Failed-handler settled cost is not observable through Host.Run; see TestRunHandlersFailurePassesSettledCostToEvent (REQ-FLOW-049).
func TestNodeCostAccruesOnCompletedHandlers(t *testing.T) {
	t.Run("success path reaches guard after accrual", func(t *testing.T) {
		def := newFlow("cost-sum")
		def.Nodes["a"] = node("agent", "", "", "")
		def.Nodes["b"] = node("agent", "", "", "")
		probe := node("tool", `{"tool":"w1"}`, "", "")
		probe.Guard = &flow.Guard{CostMpts: 65}
		def.Nodes["probe"] = probe
		addLinear(def, "a", "b", "probe")
		dc := newFakeDC()
		cat := newCatalog()
		ref := cat.register(def)
		h := host.New(cat)
		runs := map[string]int{}
		h.RegisterHandler("agent", func(_ runtime.DurableContext, n flow.Node, _ json.RawMessage) (host.NodeResult, error) {
			if err := checkHandlerRunLimit(n.Name, runs); err != nil {
				return host.NodeResult{}, err
			}
			switch n.Name {
			case "a":
				return host.NodeResult{Output: json.RawMessage(`{}`), CostMpts: 40}, nil
			case "b":
				return host.NodeResult{Output: json.RawMessage(`{}`), CostMpts: 25}, nil
			}
			return host.NodeResult{}, nil
		})
		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		if core.CodeOf(err) != core.CodeBudgetExceeded {
			t.Fatalf("want budget_exceeded at guard 65 after 65 mpts, got %v", err)
		}
		if len(dc.callLog()) != 0 {
			t.Fatalf("guard refusal made durable calls: %+v", dc.callLog())
		}
	})
	t.Run("failing handler ends the run with its error", func(t *testing.T) {
		def := newFlow("cost-fail-repair")
		def.State.Schema = json.RawMessage(`{"type":"object","properties":{"failed":{"type":"integer"}}}`)
		def.Nodes["verify"] = node("set", `{"verified":true}`, "", "")
		def.Nodes["repair"] = flow.Node{
			Kind:  "agent",
			Spec:  json.RawMessage(`{"agent":"repair"}`),
			Out:   "failed",
			Guard: &flow.Guard{CostMpts: 5000},
		}
		def.Edges = []flow.Edge{
			{From: ir.Start, To: "verify"},
			{From: "verify", To: "repair"},
			{From: "repair", To: "verify"},
		}
		dc := newFakeDC()
		cat := newCatalog()
		ref := cat.register(def)
		h := host.New(cat)
		runs := map[string]int{}
		var repairCalls int
		h.RegisterHandler("agent", func(_ runtime.DurableContext, n flow.Node, _ json.RawMessage) (host.NodeResult, error) {
			if n.Name != "repair" {
				return host.NodeResult{}, nil
			}
			if err := checkHandlerRunLimit(n.Name, runs); err != nil {
				return host.NodeResult{}, err
			}
			repairCalls++
			if repairCalls == 1 {
				return host.NodeResult{Output: json.RawMessage(`{"fixed":true}`), CostMpts: 2500}, nil
			}
			return host.NodeResult{
				Output:   json.RawMessage(`{"fixed":false}`),
				CostMpts: 2500,
			}, core.NewError(core.CodeVendor, "model failed", nil)
		})
		_, err := h.Run(dc, ref, json.RawMessage(`{"failed":1}`))
		if core.CodeOf(err) != core.CodeVendor {
			t.Fatalf("want vendor error from repair, got %v", err)
		}
		if repairCalls != 2 {
			t.Fatalf("repair ran %d times, want 2", repairCalls)
		}
	})
	t.Run("negative settled cost", func(t *testing.T) {
		def := newFlow("neg-cost")
		def.Nodes["a"] = node("agent", "", "", "")
		addLinear(def, "a")
		dc := newFakeDC()
		cat := newCatalog()
		ref := cat.register(def)
		h := host.New(cat)
		h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (host.NodeResult, error) {
			return host.NodeResult{Output: json.RawMessage(`{}`), CostMpts: -1}, nil
		})
		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("want invalid_argument, got %v", err)
		}
	})
}

func TestRepairCostGuardFailsAfterTwoHandlerVisits(t *testing.T) {
	def := newFlow("repair-loop")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"failed":{"type":"integer"}}}`)
	def.Nodes["verify"] = node("set", `{"verified":true}`, "", "")
	def.Nodes["repair"] = flow.Node{
		Kind:  "agent",
		Spec:  json.RawMessage(`{"agent":"repair"}`),
		Out:   "failed",
		Guard: &flow.Guard{CostMpts: 5000},
	}
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "verify"},
		{From: "verify", To: "repair"},
		{From: "repair", To: "verify"},
	}
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	var repairRuns int
	runs := map[string]int{}
	h.RegisterHandler("agent", func(_ runtime.DurableContext, n flow.Node, _ json.RawMessage) (host.NodeResult, error) {
		if n.Name != "repair" {
			return host.NodeResult{}, nil
		}
		if err := checkHandlerRunLimit(n.Name, runs); err != nil {
			return host.NodeResult{}, err
		}
		repairRuns++
		return host.NodeResult{Output: json.RawMessage(`{"fixed":true}`), CostMpts: 2500}, nil
	})
	_, err := h.Run(dc, ref, json.RawMessage(`{"failed":1}`))
	if repairRuns != 2 {
		t.Fatalf("repair ran %d times, want 2: %v", repairRuns, err)
	}
	if core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("code %v (%v), want budget_exceeded", core.CodeOf(err), err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "repair") || !strings.Contains(msg, "5000") || !strings.Contains(msg, "milli-points") {
		t.Fatalf("error %q, want repair, 5000 and milli-points", msg)
	}
	if len(dc.callLog()) != 0 {
		t.Fatalf("refused visit made durable calls: %+v", dc.callLog())
	}
}

func TestWithMaxFanoutClampsAboveDefault(t *testing.T) {
	items := make([]int, 10001)
	for i := range items {
		items[i] = i
	}
	payload, _ := json.Marshal(map[string]any{"items": items})
	def := newFlow("clamp-map")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"items":{"type":"array"}}}`)
	def.Nodes["fill"] = node("set", string(payload), "", "")
	def.Nodes["impl"] = flow.Node{Kind: "map", Spec: json.RawMessage(`{"over":"state.items","node":"work"}`)}
	def.Nodes["work"] = node("tool", `{"tool":"w1"}`, "", "")
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "fill"},
		{From: "fill", To: "impl"},
		{From: "impl", To: "work"},
	}
	cat := newCatalog()
	ref := cat.register(def)
	dc := newFakeDC()
	h := host.New(cat, host.WithMaxFanout(20000))
	_, err := h.Run(dc, ref, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("map of 10001: %v, want budget_exceeded", err)
	}
	if !strings.Contains(err.Error(), "impl") || !strings.Contains(err.Error(), "10001") || !strings.Contains(err.Error(), "10000") {
		t.Fatalf("error %q, want node impl, count 10001 and limit 10000", err.Error())
	}
	if len(dc.callLog()) != 0 {
		t.Fatalf("budget failure made durable calls: %+v", dc.callLog())
	}
}

func TestSubflowStepCostIsZeroInRunCost(t *testing.T) {
	child := newFlow("child")
	child.Nodes["done"] = node("set", `{"v":1}`, "", "")
	addLinear(child, "done")
	cat := newCatalog()
	cat.register(child)
	def := newFlow("parent")
	def.State.Schema = json.RawMessage(`{"type":"object","properties":{"childOut":{"type":"object"}}}`)
	def.Nodes["sub"] = node("subflow", `{"flow":"child"}`, "", "childOut")
	def.Nodes["probe"] = node("agent", "", "run.cost", "")
	def.Edges = []flow.Edge{{From: ir.Start, To: "sub"}, {From: "sub", To: "probe"}}
	ref := cat.register(def)
	dc := newFakeDC()
	dc.steps["child:child"] = scripted{result: json.RawMessage(`{"v":1}`)}
	h := host.New(cat)
	var gotCost int64
	h.RegisterHandler("agent", func(_ runtime.DurableContext, node flow.Node, input json.RawMessage) (host.NodeResult, error) {
		if node.Name != "probe" {
			return host.NodeResult{}, fmt.Errorf("unexpected %q", node.Name)
		}
		_ = json.Unmarshal(input, &gotCost)
		return host.NodeResult{}, nil
	})
	if _, err := h.Run(dc, ref, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if gotCost != 0 {
		t.Fatalf("run.cost at probe %d, want 0 after subflow", gotCost)
	}
}

func callFingerprints(calls []call) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		switch c.kind {
		case "step":
			out = append(out, fmt.Sprintf("step:%s:%s", c.name, c.stepOpts.IdempotencyKey))
		case "child":
			out = append(out, fmt.Sprintf("child:%s:%s", c.child.Name, c.childOpts.ID))
		case "gate":
			out = append(out, fmt.Sprintf("gate:%s", c.gate.Name))
		case "timer":
			out = append(out, fmt.Sprintf("timer:%s", c.duration))
		case "id":
			out = append(out, "id:"+c.childOpts.ID)
		default:
			out = append(out, c.kind)
		}
	}
	return out
}

func slicesContainsKind(fps []string, kind string) bool {
	prefix := kind + ":"
	for _, fp := range fps {
		if fp == kind || strings.HasPrefix(fp, prefix) {
			return true
		}
	}
	return false
}

func kinds(calls []call) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		switch c.kind {
		case "step":
			out[i] = "step:" + c.name
		case "timer":
			out[i] = "timer:" + c.duration.String()
		case "gate":
			out[i] = "gate:" + c.gate.Name
		case "child":
			out[i] = "child:" + c.child.Name
		}
	}
	return out
}

func waitFor(t *testing.T, dc *fakeDC, condition func(*fakeDC) bool) {
	t.Helper()
	dc.mu.Lock()
	for {
		if dc.finished {
			dc.mu.Unlock()
			t.Fatal("run finished before condition")
			return
		}
		if condition(dc) {
			dc.mu.Unlock()
			return
		}
		dc.cond.Wait()
	}
}

func assertJSON(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	if !jsonEqual(got, json.RawMessage(want)) {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func jsonEqual(left, right json.RawMessage) bool {
	var a, b any
	decoder := json.NewDecoder(bytes.NewReader(left))
	decoder.UseNumber()
	if err := decoder.Decode(&a); err != nil {
		return false
	}
	decoder = json.NewDecoder(bytes.NewReader(right))
	decoder.UseNumber()
	if err := decoder.Decode(&b); err != nil {
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
