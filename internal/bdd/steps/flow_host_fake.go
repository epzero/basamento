package steps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

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
}

type hostFuture struct {
	done   bool
	result json.RawMessage
	err    error
	dc     *hostFake
	step   string
}

func (f *hostFuture) Get(out any) error {
	f.dc.mu.Lock()
	if f.step != "" {
		f.dc.recordFutureGet(f.step)
	}
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

	selectPreferStep string
	awaitedGets      []string
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

func (d *hostFake) recordFutureGet(step string) {
	d.awaitedGets = append(d.awaitedGets, step)
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
	if script.failTimes > 0 && script.err != nil {
		if d.executed[name] <= script.failTimes {
			execErr = script.err
		} else {
			result = append(json.RawMessage(nil), script.result...)
		}
	} else if script.failTimes > 0 {
		for i := 0; i < script.failTimes; i++ {
			d.executed[name]++
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
	return &hostFuture{dc: d, done: true, result: result, err: execErr, step: name}
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
		var ready []int
		for i, candidate := range futures {
			if candidate.(*hostFuture).done {
				ready = append(ready, i)
			}
		}
		if len(ready) > 0 {
			if d.selectPreferStep != "" {
				for _, i := range ready {
					if futures[i].(*hostFuture).step == d.selectPreferStep {
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

func (d *hostFake) Emit(runtime.ProgressEvent) error {
	d.mu.Lock()
	d.emits++
	d.record(hostCall{kind: "emit"})
	d.mu.Unlock()
	return nil
}

func (d *hostFake) Checkpoint(runtime.SnapshotRef) error {
	d.mu.Lock()
	d.checks++
	d.record(hostCall{kind: "checkpoint"})
	d.mu.Unlock()
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

func (d *hostFake) requireJournalFullyConsumed() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.journalIndex != len(d.journal) {
		return fmt.Errorf("replay consumed %d journal entries, want all %d", d.journalIndex, len(d.journal))
	}
	return nil
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
