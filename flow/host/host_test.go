package host_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
)

// --- a recording, scripted DurableContext ---------------------------------

type call struct {
	kind     string
	name     string
	input    json.RawMessage
	duration time.Duration
	gate     runtime.GateSpec
	child    runtime.FlowRef
}

type scripted struct {
	result json.RawMessage
	err    error
	manual bool
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

func (f *future) Ready() bool {
	f.dc.mu.Lock()
	defer f.dc.mu.Unlock()
	return f.done
}

type fakeDC struct {
	mu      sync.Mutex
	cond    *sync.Cond
	notify  chan struct{}
	now     time.Time
	calls   []call
	steps   map[string]scripted
	gates   map[string]*future
	timers  []timedFuture
	manual  map[string]*future
	emits   []runtime.ProgressEvent
	checks  []runtime.SnapshotRef
	nextID  int
	instant bool
	stepRun map[string]int
}

type timedFuture struct {
	deadline time.Time
	future   *future
	gate     bool
}

func newFakeDC() *fakeDC {
	d := &fakeDC{
		notify:  make(chan struct{}, 1),
		now:     time.Unix(1_700_000_000, 0).UTC(),
		steps:   map[string]scripted{},
		gates:   map[string]*future{},
		manual:  map[string]*future{},
		stepRun: map[string]int{},
	}
	d.cond = sync.NewCond(&d.mu)
	return d
}

func (d *fakeDC) record(c call) {
	d.mu.Lock()
	d.calls = append(d.calls, c)
	d.mu.Unlock()
	d.signal()
}

func (d *fakeDC) signal() {
	select {
	case d.notify <- struct{}{}:
	default:
	}
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
	d.nextID++
	return fmt.Sprintf("%s-%d", kind, d.nextID)
}

func (d *fakeDC) Rand(key string) *rand.Rand {
	return rand.New(rand.NewPCG(1, 2))
}

func (d *fakeDC) Step(name string, in any, o runtime.StepOptions) runtime.Future {
	d.record(call{kind: "step", name: name, input: decodeArgument(in)})
	d.mu.Lock()
	defer d.mu.Unlock()
	script, ok := d.steps[name]
	if !ok {
		script = scripted{result: json.RawMessage(`{}`)}
	}
	if script.manual {
		f := &future{dc: d}
		d.manual[name] = f
		d.signal()
		return f
	}
	d.stepRun[name]++
	return &future{dc: d, done: true, result: script.result, err: script.err}
}

func (d *fakeDC) Child(flowRef runtime.FlowRef, in any, o runtime.ChildOptions) runtime.Future {
	d.record(call{kind: "child", child: flowRef, input: decodeArgument(in)})
	d.mu.Lock()
	defer d.mu.Unlock()
	script := d.steps["child:"+flowRef.Name]
	return &future{dc: d, done: true, result: script.result, err: script.err}
}

func (d *fakeDC) Sleep(duration time.Duration) runtime.Future {
	d.record(call{kind: "timer", duration: duration})
	d.mu.Lock()
	defer d.mu.Unlock()
	f := &future{dc: d}
	if d.instant {
		f.done = true
		return f
	}
	d.timers = append(d.timers, timedFuture{deadline: d.now.Add(duration), future: f})
	return f
}

func (d *fakeDC) Await(spec runtime.GateSpec) runtime.Future {
	d.record(call{kind: "gate", gate: spec})
	d.mu.Lock()
	defer d.mu.Unlock()
	f := &future{dc: d}
	d.gates[spec.Name] = f
	if spec.Timeout > 0 {
		d.timers = append(d.timers, timedFuture{deadline: d.now.Add(spec.Timeout), future: f, gate: true})
	}
	return f
}

func (d *fakeDC) Select(futures ...runtime.Future) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for {
		for i, candidate := range futures {
			f := candidate.(*future)
			if f.done {
				return i, nil
			}
		}
		d.cond.Wait()
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
		} else {
			timer.future.done = true
		}
	}
	d.timers = kept
	d.cond.Broadcast()
	d.signal()
}

func (d *fakeDC) resolveGate(name string, payload json.RawMessage) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.gates[name]
	f.done = true
	f.result = payload
	d.cond.Broadcast()
	d.signal()
}

func (d *fakeDC) resolveStep(name string, payload json.RawMessage, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	f := d.manual[name]
	f.done = true
	f.result = payload
	f.err = err
	d.cond.Broadcast()
	d.signal()
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
	h.RegisterHandler("agent", func(_ runtime.DurableContext, n flow.Node, input json.RawMessage) (json.RawMessage, error) {
		gotNode = n
		gotInput = append(json.RawMessage(nil), input...)
		return json.RawMessage(`{"said":"done"}`), nil
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

	done := make(chan error, 1)
	go func() {
		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		done <- err
	}()
	waitFor(t, dc, func() bool { return len(dc.callLog()) == 1 })
	if calls := dc.callLog(); calls[0].kind != "timer" || calls[0].duration != 30*time.Second {
		t.Fatalf("calls %+v, want one 30s timer", calls)
	}
	dc.advance(29 * time.Second)
	select {
	case err := <-done:
		t.Fatalf("run finished after 29s: %v", err)
	default:
	}
	dc.advance(1 * time.Second)
	if err := <-done; err != nil {
		t.Fatalf("run after 30s: %v", err)
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

	done := make(chan error, 1)
	go func() {
		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		done <- err
	}()
	waitFor(t, dc, func() bool { return len(dc.callLog()) == 1 })
	spec := dc.callLog()[0].gate
	if spec.Name != "approve" || spec.AcceptIf != "input.approved" || spec.Timeout != 72*time.Hour {
		t.Fatalf("gate spec %+v", spec)
	}
	assertJSON(t, spec.Schema, `"review.json"`)
	dc.resolveGate("approve", json.RawMessage(`{"approved":true}`))
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}

func TestGateTimeoutFailsRun(t *testing.T) {
	def := newFlow("approve")
	def.Nodes["approve"] = node("gate", `{"schema":"review.json","accept_if":"input.approved","timeout":"72h"}`, "", "")
	addLinear(def, "approve")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)

	done := make(chan error, 1)
	go func() {
		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		done <- err
	}()
	waitFor(t, dc, func() bool { return len(dc.callLog()) == 1 })
	dc.advance(72 * time.Hour)
	if err := <-done; core.CodeOf(err) != core.CodeTimeout {
		t.Fatalf("code %v (%v), want timeout", core.CodeOf(err), err)
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

func TestHandlerFailureFailsRunAndStopsLaterNodes(t *testing.T) {
	def := newFlow("handled")
	def.Nodes["plan"] = node("agent", `{"agent":"p"}`, "", "")
	def.Nodes["review"] = node("tool", `{"tool":"t1"}`, "", "")
	addLinear(def, "plan", "review")
	dc := newFakeDC()
	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (json.RawMessage, error) {
		return nil, core.NewError(core.CodeVendor, "model vendor failed", nil)
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
	h.RegisterHandler("agent", func(runtime.DurableContext, flow.Node, json.RawMessage) (json.RawMessage, error) {
		return nil, errors.New("unexpected panic")
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

	done := make(chan error, 1)
	go func() {
		_, err := h.Run(dc, ref, json.RawMessage(`{}`))
		done <- err
	}()
	waitFor(t, dc, func() bool { return len(dc.callLog()) == 2 })
	calls := dc.callLog()
	if calls[0].duration != 10*time.Second || calls[1].duration != 30*time.Second {
		t.Fatalf("timers %+v, want 10s then 30s in node-id order", calls)
	}
	dc.advance(10 * time.Second)
	select {
	case err := <-done:
		t.Fatalf("run finished with a timer pending: %v", err)
	default:
	}
	dc.advance(20 * time.Second)
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
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
	first.steps["a"] = scripted{result: json.RawMessage(`{"t":1}`)}
	first.steps["c_step"] = scripted{result: json.RawMessage(`{"ok":true}`)}
	replay := newFakeDC()
	replay.instant = true
	replay.steps["a"] = scripted{result: json.RawMessage(`{"t":1}`)}
	replay.steps["c_step"] = scripted{result: json.RawMessage(`{"ok":true}`), manual: true}

	cat := newCatalog()
	ref := cat.register(def)
	h := host.New(cat)
	h.RegisterHandler("agent", func(dc runtime.DurableContext, n flow.Node, input json.RawMessage) (json.RawMessage, error) {
		var out json.RawMessage
		if err := dc.Step("c_step", input, runtime.StepOptions{}).Get(&out); err != nil {
			return nil, err
		}
		return out, nil
	})

	if _, err := h.Run(first, ref, json.RawMessage(`{}`)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// The replayed run reaches c_step's history entry without executing it.
	replayed := make(chan error, 1)
	go func() {
		_, err := h.Run(replay, ref, json.RawMessage(`{}`))
		replayed <- err
	}()
	waitFor(t, replay, func() bool {
		replay.mu.Lock()
		defer replay.mu.Unlock()
		_, ok := replay.manual["c_step"]
		return ok
	})
	replay.resolveStep("c_step", json.RawMessage(`{"ok":true}`), nil)
	if err := <-replayed; err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got, want := kinds(replay.callLog()), kinds(first.callLog()); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("replay calls %v, want %v", got, want)
	}
	if count := replay.stepExecutions()["c_step"]; count != 0 {
		t.Fatalf("c_step ran %d times in replay, want it taken from history", count)
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
		done := make(chan json.RawMessage, 1)
		errs := make(chan error, 1)
		go func() {
			out, err := h.Run(dc, ref, json.RawMessage(`{}`))
			if err != nil {
				errs <- err
				return
			}
			done <- out
		}()
		waitFor(t, dc, func() bool {
			dc.mu.Lock()
			defer dc.mu.Unlock()
			return len(dc.manual) == 3
		})
		order := []string{"a", "b", "c"}
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		for _, id := range order {
			payload, _ := json.Marshal(id)
			dc.resolveStep(id, payload, nil)
		}
		select {
		case err := <-errs:
			t.Fatalf("round %d: %v", round, err)
		case out := <-done:
			if want == nil {
				want = out
				continue
			}
			if !jsonEqual(want, out) {
				t.Fatalf("round %d: state %s, want %s", round, out, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d: run did not complete", round)
		}
	}
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

func waitFor(t *testing.T, dc *fakeDC, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for !condition() {
		select {
		case <-dc.notify:
		case <-deadline.C:
			t.Fatal("condition not met before timeout")
		}
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
