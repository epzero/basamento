package local

import (
	"fmt"

	"github.com/epzero/basamento/runtime"
)

func childPolicy(parentClose string) (string, error) {
	policy := parentClose
	if policy == "" {
		policy = defaultParentClose
	}
	switch policy {
	case "terminate", "request_cancel", "abandon":
		return policy, nil
	default:
		return "", invalidErr(fmt.Sprintf("local: unknown parent close policy %q", policy), map[string]any{"policy": policy})
	}
}

// recordedOrClosedChildFuture returns a future when the parent already recorded
// a child for id and flow, or when the parent is not running or is shutting
// down. The caller must hold r.mu.
func (r *run) recordedOrClosedChildFuture(id string, flow runtime.FlowRef) (*future, bool) {
	for _, link := range r.children {
		if link.run.id == id && link.run.flow == flow {
			return link.future, true
		}
	}
	if r.state != stateRunning || r.shuttingDown {
		f := newFuture(r)
		f.complete(nil, cancelledErr("local: the parent is closed"))
		return f, true
	}
	return nil, false
}

// linkChild attaches child to the parent journal or returns an existing link.
// The caller must not hold r.mu.
func (r *run) linkChild(child *run, policy string) *future {
	r.mu.Lock()
	for _, link := range r.children {
		if link.run == child {
			r.mu.Unlock()
			return link.future
		}
	}
	f := newFuture(r)
	child.mu.Lock()
	if child.state == stateRunning {
		child.waiters = append(child.waiters, f)
	}
	out, childErr := child.output, child.err
	state := child.state
	child.mu.Unlock()
	if state != stateRunning {
		f.complete(out, childErr)
	}

	if r.state == stateRunning && !r.shuttingDown {
		r.children = append(r.children, &childLink{run: child, policy: policy, future: f})
		r.mu.Unlock()
		return f
	}
	r.mu.Unlock()
	child.applyParentClose(policy)
	f.complete(nil, cancelledErr("local: the parent closed before the child was linked"))
	return f
}
