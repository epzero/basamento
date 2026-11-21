package temporal

import (
	"encoding/json"
	"fmt"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// ResolveGateUpdate is the Temporal Update name for gate resolution.
const ResolveGateUpdate = "basamento-resolve-gate"

// GateResolution is the Update argument for resolving a gate by name.
type GateResolution struct {
	Gate    string          `json:"gate"`
	Payload json.RawMessage `json:"payload"`
}

type gateState struct {
	spec        runtime.GateSpec
	wfFut       workflow.Future
	set         workflow.Settable
	settle      bool
	err         error
	payload     json.RawMessage
	timerCancel workflow.CancelFunc
}

// Await registers or returns a gate wait. GateSpec.Schema and GateSpec.AcceptIf
// are not evaluated here (FLOW-006-01, WP-L5-04): only the gate name matters for
// registration; the Update accepts any payload shape.
func (dc *durableContext) Await(spec runtime.GateSpec) runtime.Future {
	if dc.continuing {
		return newImmediate(cancelledContinuing())
	}
	if dc.wfCtx.Err() != nil {
		return dc.awaitAfterCancel(spec)
	}
	if existing := dc.gates[spec.Name]; existing != nil {
		return &gateFuture{ctx: dc.wfCtx, g: existing}
	}
	if j, ok := dc.cont.Gates[spec.Name]; ok {
		if j.Resolved {
			return newImmediateResult(j.Payload)
		}
		if j.EndedByTimeout || j.EndedByCancel {
			return newImmediate(journalGateErr(j))
		}
	}
	wfFut, set := workflow.NewFuture(dc.wfCtx)
	g := &gateState{spec: spec, wfFut: wfFut, set: set}
	dc.gates[spec.Name] = g
	j := dc.ensureGateJournal(spec)
	dc.armGateTimeoutFromJournal(spec.Name, j)
	return &gateFuture{ctx: dc.wfCtx, g: g}
}

func (dc *durableContext) awaitAfterCancel(spec runtime.GateSpec) runtime.Future {
	wfFut, set := workflow.NewFuture(dc.wfCtx)
	err := core.NewError(core.CodeCancelled, "temporal: gate cancelled", nil)
	g := &gateState{spec: spec, wfFut: wfFut, set: set, settle: true, err: err}
	dc.gates[spec.Name] = g
	set.SetError(err)
	dc.ensureGateJournal(spec)
	j := dc.cont.Gates[spec.Name]
	j.EndedByCancel = true
	dc.cont.Gates[spec.Name] = j
	return &gateFuture{ctx: dc.wfCtx, g: g}
}

func (dc *durableContext) ensureGateJournal(spec runtime.GateSpec) GateJournal {
	dc.cont.ensure()
	if j, ok := dc.cont.Gates[spec.Name]; ok {
		return j
	}
	j := GateJournal{Name: spec.Name, TimeoutNs: spec.Timeout.Nanoseconds()}
	if spec.Timeout > 0 {
		j.Deadline = workflow.Now(dc.wfCtx).Add(spec.Timeout)
	}
	dc.cont.Gates[spec.Name] = j
	return j
}

func (dc *durableContext) armGateTimeoutFromJournal(name string, j GateJournal) {
	if j.TimeoutNs <= 0 {
		return
	}
	remaining := j.Deadline.Sub(workflow.Now(dc.wfCtx))
	if remaining <= 0 {
		dc.settleGate(name, nil, gateTimeoutErr(name))
		return
	}
	dc.armGateTimeout(name, remaining)
}

func (dc *durableContext) armGateTimeout(name string, d time.Duration) {
	g := dc.gates[name]
	if g == nil || g.settle {
		return
	}
	timerCtx, cancel := workflow.WithCancel(dc.wfCtx)
	g.timerCancel = cancel
	tf := workflow.NewTimer(timerCtx, d)
	workflow.Go(dc.wfCtx, func(ctx workflow.Context) {
		dc.onGateTimer(ctx, name, tf)
	})
}

func (dc *durableContext) onGateTimer(ctx workflow.Context, name string, tf workflow.Future) {
	if err := tf.Get(ctx, nil); err != nil {
		return
	}
	dc.settleGate(name, nil, gateTimeoutErr(name))
}

func (dc *durableContext) settleGate(name string, payload json.RawMessage, err error) {
	g := dc.gates[name]
	if g == nil || g.settle {
		return
	}
	g.settle = true
	g.payload = append(json.RawMessage(nil), payload...)
	g.err = err
	if g.timerCancel != nil {
		g.timerCancel()
		g.timerCancel = nil
	}
	if err != nil {
		g.set.SetError(err)
	} else {
		g.set.Set(payload, nil)
	}
	dc.cont.ensure()
	j := dc.cont.Gates[name]
	if j.Name == "" {
		j.Name = name
	}
	j.Resolved = err == nil
	j.Payload = append(json.RawMessage(nil), payload...)
	if err != nil {
		if core.CodeOf(err) == core.CodeTimeout {
			j.EndedByTimeout = true
		} else if core.CodeOf(err) == core.CodeCancelled {
			j.EndedByCancel = true
		}
	}
	dc.cont.Gates[name] = j
}

func (dc *durableContext) registerGateUpdateHandler() error {
	return workflow.SetUpdateHandlerWithOptions(dc.wfCtx, ResolveGateUpdate,
		func(ctx workflow.Context, res GateResolution) error {
			dc.handleGateResolve(res)
			return nil
		},
		workflow.UpdateHandlerOptions{
			Validator: func(ctx workflow.Context, res GateResolution) error {
				return dc.validateGateResolve(res)
			},
		})
}

func (dc *durableContext) validateGateResolve(res GateResolution) error {
	if dc.continuing {
		return validatorErr(core.CodeUnavailable, "temporal: the run is continuing as new; retry")
	}
	j, inJournal := dc.cont.Gates[res.Gate]
	if !inJournal {
		return validatorErr(core.CodeNotFound, "temporal: no gate with that name on the run")
	}
	if j.Resolved {
		return validatorErr(core.CodeConflict, "temporal: the gate was already resolved")
	}
	if j.EndedByTimeout || j.EndedByCancel {
		return validatorErr(core.CodeConflict, "temporal: the gate wait already ended")
	}
	return nil
}

func (dc *durableContext) handleGateResolve(res GateResolution) {
	g := dc.gates[res.Gate]
	if g == nil || g.settle {
		return
	}
	dc.settleGate(res.Gate, res.Payload, nil)
}

func validatorErr(code core.Code, msg string) error {
	return temporal.NewNonRetryableApplicationError(msg, string(code), nil)
}

func gateTimeoutErr(name string) error {
	return core.NewError(core.CodeTimeout, fmt.Sprintf("temporal: gate %q timed out", name), map[string]any{"gate": name})
}

func journalGateErr(j GateJournal) error {
	if j.Resolved {
		return nil
	}
	if j.EndedByTimeout {
		return gateTimeoutErr(j.Name)
	}
	if j.EndedByCancel {
		return core.NewError(core.CodeCancelled, "temporal: gate cancelled", nil)
	}
	return nil
}

func (dc *durableContext) startGateCancelWatcher() {
	workflow.Go(dc.wfCtx, func(ctx workflow.Context) {
		selector := workflow.NewSelector(ctx)
		selector.AddReceive(ctx.Done(), func(workflow.ReceiveChannel, bool) {
			for name, g := range dc.gates {
				if g != nil && !g.settle {
					dc.settleGate(name, nil, core.NewError(core.CodeCancelled, "temporal: gate cancelled", nil))
				}
			}
		})
		selector.Select(ctx)
	})
}
