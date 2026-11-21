package temporal

import (
	"context"
	"encoding/json"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// stepActivityInput is the activity argument for one step invocation.
type stepActivityInput struct {
	Name string              `json:"name"`
	In   json.RawMessage     `json:"in"`
	Opts runtime.StepOptions `json:"opts"`
}

// runStepActivity executes one registered step with tenant context and panic
// recovery (REQ-RT-017).
func (r *Runtime) runStepActivity(ctx context.Context, in stepActivityInput) (json.RawMessage, error) {
	r.mu.Lock()
	reg, ok := r.steps[in.Name]
	r.mu.Unlock()
	if !ok {
		return nil, invalidErr("temporal: step is not registered", map[string]any{"step": in.Name})
	}
	var out json.RawMessage
	var err error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = stepPanicErr(in.Name)
				tenant, _ := core.TenantFrom(ctx)
				r.logPanic(tenant, "", in.Name, recovered)
			}
		}()
		out, err = reg.fn(ctx, in.In)
	}()
	if err != nil {
		return nil, activityApplicationError(err)
	}
	return out, nil
}
