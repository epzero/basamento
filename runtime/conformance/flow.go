package conformance

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// StepNames are every step the golden flows may call. The harness registers
// them all with the runtime, so a flow never fails because a step of another
// flow is missing.
var StepNames = func() []string {
	names := []string{"s1", "s2", "s3", "s4", "s4-mutated", "a", "b", "merge"}
	for i := 1; i <= 40; i++ {
		names = append(names, fmt.Sprintf("c%d", i))
	}
	return names
}()

// Spec describes one golden flow.
type Spec struct {
	// Flow is the conformance flow name.
	Flow string
	// Gate is the gate the flow waits at, if any.
	Gate string
	// Timeout is the gate's timeout. The harness advances the clock past it
	// when the flow reports the gate wait, so the timeout fires deterministically.
	Timeout time.Duration
}

// Specs lists the golden flows the scenarios and the suite use.
var Specs = map[string]Spec{
	"linear":           {Flow: "linear"},
	"parallel-merge":   {Flow: "parallel-merge"},
	"gate-and-timeout": {Flow: "gate-and-timeout", Gate: "approve", Timeout: 2 * time.Hour},
	// continue-as-new has forty steps and one checkpoint: with a checkpoint
	// threshold below that length the temporal runtime continues the run as
	// new, and the record proves the run kept its steps, outbox and id
	// (RT-036-01, RT-038-01).
	"continue-as-new": {Flow: "continue-as-new"},
}

// builder writes the flow code of one conformance flow against the durable
// context. It records what it emits so the record is runtime-neutral.
type builder func(h *Harness, dc runtime.DurableContext, in json.RawMessage) (json.RawMessage, error)

var builders = map[string]builder{
	"linear": func(h *Harness, dc runtime.DurableContext, in json.RawMessage) (json.RawMessage, error) {
		return linear(h, dc, in, "s4")
	},
	"linear-mutated": func(h *Harness, dc runtime.DurableContext, in json.RawMessage) (json.RawMessage, error) {
		return linear(h, dc, in, "s4-mutated")
	},
	"parallel-merge":   parallelMerge,
	"gate-and-timeout": gateAndTimeout,
	"continue-as-new":  continueAsNew,
}

// linear is four steps with one durable event between each, then a closing
// event: nine durable calls, so event 7 is the fourth step and a crash after
// step 2 leaves steps 1 and 2 recorded (RT-001-01, RT-002-01, RT-004-01).
func linear(h *Harness, dc runtime.DurableContext, _ json.RawMessage, fourth string) (json.RawMessage, error) {
	if err := h.emit(dc, "started", "linear"); err != nil {
		return nil, err
	}
	total := 0
	for i := 1; i <= 4; i++ {
		name := fmt.Sprintf("s%d", i)
		if i == 4 {
			name = fourth
		}
		value, err := h.step(dc, name)
		if err != nil {
			return nil, err
		}
		total += value
		if err := h.emit(dc, name+"_done", "linear"); err != nil {
			return nil, err
		}
	}
	return marshal(map[string]any{"flow": "linear", "total": total})
}

// parallelMerge runs two steps at once and waits for both, then merges them.
// The order the steps finish in is the runtime's; the record keeps counts, not
// order, so both runtimes compare equal (RT-001-01).
func parallelMerge(h *Harness, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
	for _, name := range []string{"s1", "s2"} {
		if _, err := h.step(dc, name); err != nil {
			return nil, err
		}
	}
	fa := dc.Step("a", nil, runtime.StepOptions{})
	fb := dc.Step("b", nil, runtime.StepOptions{})
	pending := []runtime.Future{fa, fb}
	for len(pending) > 0 {
		index, err := dc.Select(pending...)
		if err != nil {
			return nil, err
		}
		future := pending[index]
		pending = append(pending[:index], pending[index+1:]...)
		var out json.RawMessage
		if err := future.Get(&out); err != nil {
			return nil, err
		}
	}
	if _, err := h.step(dc, "merge"); err != nil {
		return nil, err
	}
	if err := h.emit(dc, "merged", "parallel-merge"); err != nil {
		return nil, err
	}
	return marshal(map[string]any{"flow": "parallel-merge", "merged": true})
}

// gateAndTimeout waits at a gate with a timeout; the harness advances the clock
// past the timeout, the wait fails with code timeout, and the flow records the
// outcome (RT-001-01).
func gateAndTimeout(h *Harness, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
	if _, err := h.step(dc, "s1"); err != nil {
		return nil, err
	}
	wait := dc.Await(runtime.GateSpec{Name: "approve", Timeout: 2 * time.Hour})
	h.gateAwaited()
	var payload json.RawMessage
	err := wait.Get(&payload)
	if err != nil && core.CodeOf(err) != core.CodeTimeout {
		return nil, err
	}
	if err != nil {
		if emitErr := h.emit(dc, "gate_timeout", "gate-and-timeout"); emitErr != nil {
			return nil, emitErr
		}
		if _, stepErr := h.step(dc, "s2"); stepErr != nil {
			return nil, stepErr
		}
		return marshal(map[string]any{"flow": "gate-and-timeout", "outcome": "timeout"})
	}
	return append(json.RawMessage(nil), payload...), nil
}

// continueAsNew runs forty steps and checkpoints once: the checkpoint is the
// only point a run may continue as new, and with a low threshold the temporal
// runtime does exactly once (RT-036-01, RT-038-01).
func continueAsNew(h *Harness, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
	for i := 1; i <= 40; i++ {
		if _, err := h.step(dc, fmt.Sprintf("c%d", i)); err != nil {
			return nil, err
		}
	}
	if err := dc.Checkpoint(runtime.SnapshotRef{Seq: 1}); err != nil {
		return nil, err
	}
	if err := h.emit(dc, "continued", "continue-as-new"); err != nil {
		return nil, err
	}
	return marshal(map[string]any{"flow": "continue-as-new", "steps": 40})
}

func marshal(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// stepValue is a step's deterministic result. The number in the name makes the
// output differ per step, so a run that skipped one would not look identical.
func stepValue(name string) json.RawMessage {
	n := 0
	for _, r := range name {
		if r >= '0' && r <= '9' {
			n = n*10 + int(r-'0')
		}
	}
	raw, _ := json.Marshal(map[string]any{"step": name, "value": n})
	return raw
}

// fail returns a coded error for a step that cannot run.
func fail(message string) error {
	return core.NewError(core.CodeInternal, message, nil)
}
