package temporal

import (
	"context"
	"encoding/json"
	"fmt"

	"go.temporal.io/sdk/activity"

	"github.com/epzero/basamento/core"
)

// StepActivityInput is the activity argument for one step invocation.
type StepActivityInput struct {
	Name string          `json:"name"`
	In   json.RawMessage `json:"in"`
}

func workflowIDFromActivity(ctx context.Context) string {
	if activity.IsActivity(ctx) {
		return activity.GetInfo(ctx).WorkflowExecution.ID
	}
	return ""
}

// runStepActivity executes one registered step with tenant context and panic
// recovery (REQ-RT-017).
func (r *Runtime) runStepActivity(ctx context.Context, in StepActivityInput) (json.RawMessage, error) {
	reg, ok := r.stepFor(in.Name)
	if !ok {
		return nil, activityApplicationError(core.NewError(core.CodeUnsupported,
			fmt.Sprintf("temporal: step %q is not registered", in.Name), map[string]any{"step": in.Name}))
	}
	tenant, _ := core.TenantFrom(ctx)
	wfID := workflowIDFromActivity(ctx)
	var out json.RawMessage
	var err error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = stepPanicErr(in.Name)
				r.logPanic(tenant, wfID, in.Name, recovered)
			}
		}()
		out, err = reg.fn(ctx, in.In)
	}()
	if err != nil {
		r.logStepFailure(tenant, wfID, in.Name, err)
		return nil, activityApplicationError(err)
	}
	return out, nil
}
