package host_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/runtime"
)

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
