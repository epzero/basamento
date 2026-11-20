package host_test

import (
	"encoding/json"
	"time"

	"github.com/epzero/basamento/core"
)

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
