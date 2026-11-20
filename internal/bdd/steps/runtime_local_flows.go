package steps

import (
	"encoding/json"
	"time"

	"github.com/epzero/basamento/runtime"
)

// --- flow scripts ----------------------------------------------------------

// stepFlow calls one step and returns its result; an unhandled error fails the
// run, as the contract says.
func (w *localWorld) stepFlow(name string) localFlow {
	return func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		if err := w.ensureStep(dc.Context(), name); err != nil {
			return nil, err
		}
		var out json.RawMessage
		err := dc.Step(name, nil, w.options(name)).Get(&out)
		e.update(func() { e.stepErr = err })
		if err != nil {
			return nil, err
		}
		return out, nil
	}
}

func (w *localWorld) twiceFlow(name string) localFlow {
	return func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		if err := w.ensureStep(dc.Context(), name); err != nil {
			return nil, err
		}
		var first, second json.RawMessage
		if err := dc.Step(name, nil, w.options(name)).Get(&first); err != nil {
			return nil, err
		}
		if err := dc.Step(name, nil, w.options(name)).Get(&second); err != nil {
			return nil, err
		}
		e.update(func() {
			e.first = first
			e.second = second
		})
		return json.RawMessage(`{}`), nil
	}
}

func (w *localWorld) blockingStepFlow(name string) localFlow {
	return func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		if err := w.ensureStep(dc.Context(), name); err != nil {
			return nil, err
		}
		var out json.RawMessage
		err := dc.Step(name, nil, w.options(name)).Get(&out)
		e.update(func() { e.stepErr = err })
		return nil, err
	}
}

func (w *localWorld) gateFlow(gate string) localFlow {
	return func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Await(runtime.GateSpec{Name: gate})
		e.update(func() {
			e.waiting = true
			e.atGate = gate
		})
		var payload json.RawMessage
		if err := wait.Get(&payload); err != nil {
			e.update(func() { e.waitErr = err })
			return nil, err
		}
		return payload, nil
	}
}

func (w *localWorld) timerFlow(d time.Duration) localFlow {
	return func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		wait := dc.Sleep(d)
		e.update(func() { e.waiting = true })
		if err := wait.Get(nil); err != nil {
			e.update(func() { e.waitErr = err })
			return nil, err
		}
		e.update(func() { e.timers++ })
		return json.RawMessage(`{}`), nil
	}
}
