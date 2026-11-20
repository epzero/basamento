package steps

import (
	"encoding/json"
	"fmt"

	"github.com/epzero/basamento/runtime"
)

// Defaults used only when the scenario text does not name a value (see local.feature RT-049…050).
const (
	localDefaultChildFlow   = "child"
	localDefaultParentFlow  = "parent"
	localDefaultClosePolicy = "abandon"
)

// localChildCall records one dc.Child invocation from the parent flow goroutine.
type localChildCall struct {
	flow     string
	id       string
	ready    bool
	readyErr error
}

// localChildCommand asks a command-driven parent to issue another Child call.
type localChildCommand struct {
	flow        string
	id          string
	parentClose string
}

// parentChildReg remembers how a parent flow was registered to start a child.
type parentChildReg struct {
	child, id, policy string
	await             bool
}

func (w *localWorld) rememberParentChild(parent, child, id, policy string, await bool) {
	w.mu.Lock()
	if w.parentRegs == nil {
		w.parentRegs = map[string]parentChildReg{}
	}
	w.parentRegs[parent] = parentChildReg{child: child, id: id, policy: policy, await: await}
	w.mu.Unlock()
}

func (w *localWorld) parentReg(parent string) (parentChildReg, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	reg, ok := w.parentRegs[parent]
	return reg, ok
}

func (e *localExec) sendChildCommand(cmd localChildCommand) error {
	select {
	case e.commands <- cmd:
		return nil
	default:
		return fmt.Errorf("parent %q is not waiting for a child command", e.name)
	}
}

func (w *localWorld) recordChildCall(e *localExec, flow, id string, fut runtime.Future) {
	ready := fut.Ready()
	var readyErr error
	if ready {
		readyErr = fut.Get(nil)
	}
	e.update(func() {
		e.childCalls = append(e.childCalls, localChildCall{
			flow: flow, id: id, ready: ready, readyErr: readyErr,
		})
	})
}

// commandParentFlow keeps the parent run alive, optionally starting one child
// then looping on commands until the run context ends.
func (w *localWorld) commandParentFlow(initial *parentChildReg) localFlow {
	return func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		if initial != nil {
			opts := runtime.ChildOptions{ID: initial.id, ParentClose: initial.policy}
			fut := dc.Child(runtime.FlowRef{Name: initial.child}, nil, opts)
			w.recordChildCall(e, initial.child, initial.id, fut)
		}
		for {
			select {
			case cmd := <-e.commands:
				opts := runtime.ChildOptions{ID: cmd.id, ParentClose: cmd.parentClose}
				fut := dc.Child(runtime.FlowRef{Name: cmd.flow}, nil, opts)
				w.recordChildCall(e, cmd.flow, cmd.id, fut)
			case <-dc.Context().Done():
				return nil, dc.Context().Err()
			}
		}
	}
}

// setParentFlow scripts a parent that starts a child with the given id. With
// await it waits for and records the child's output; otherwise it returns at
// once so the close policy is the only thing that ends the child.
func (w *localWorld) setParentFlow(name, child, childID, policy string, await bool) {
	w.rememberParentChild(name, child, childID, policy, await)
	w.setFlow(name, func(e *localExec, dc runtime.DurableContext, _ json.RawMessage) (json.RawMessage, error) {
		options := runtime.ChildOptions{ID: childID, ParentClose: policy}
		future := dc.Child(runtime.FlowRef{Name: child}, nil, options)
		if !await {
			return json.RawMessage(`{}`), nil
		}
		var out json.RawMessage
		if err := future.Get(&out); err != nil {
			return nil, err
		}
		e.update(func() { e.childOut = out })
		return out, nil
	})
}

func (w *localWorld) setCommandParent(parent string, initial *parentChildReg) {
	w.setFlow(parent, w.commandParentFlow(initial))
}
