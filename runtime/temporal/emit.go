package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

const emitActivityName = "basamento-emit"

// emitActivityInput is the activity argument for one Emit call.
type emitActivityInput struct {
	RunID string          `json:"run_id"`
	Seq   uint64          `json:"seq"`
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data"`
}

func emitRetryPolicy() *temporal.RetryPolicy {
	return &temporal.RetryPolicy{
		InitialInterval:    time.Second,
		BackoffCoefficient: 2.0,
		MaximumInterval:    time.Minute,
		MaximumAttempts:    0,
	}
}

func (dc *durableContext) emitReplay(call uint64) (done bool, err error) {
	for _, f := range dc.cont.EmitFailures {
		if f.Call == call {
			return true, journalToErr(&f.Err)
		}
	}
	failuresBefore := 0
	for _, f := range dc.cont.EmitFailures {
		if f.Call < call {
			failuresBefore++
		}
	}
	successOrdinal := call - uint64(failuresBefore)
	if successOrdinal <= dc.cont.Emitted {
		return true, nil
	}
	return false, nil
}

func (dc *durableContext) Emit(ev runtime.ProgressEvent) error {
	if dc.continuing {
		return cancelledContinuing()
	}
	dc.emitCalls++
	call := dc.emitCalls
	if done, err := dc.emitReplay(call); done {
		return err
	}
	// A terminal failure consumes its seq: a later, different event must not
	// reuse it, because a store-then-error sink may already hold it.
	seq := dc.cont.Emitted + uint64(len(dc.cont.EmitFailures)) + 1
	raw := append(json.RawMessage(nil), ev.Data...)
	actCtx := workflow.WithActivityOptions(dc.wfCtx, workflow.ActivityOptions{
		TaskQueue:           dc.rt.taskQueue,
		StartToCloseTimeout: time.Minute,
		RetryPolicy:         emitRetryPolicy(),
	})
	var early error
	var actErr error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if _, ok := recovered.(taskFailure); ok {
					panic(recovered)
				}
				var pe *core.Error
				if errors.As(errorFromPanic(recovered), &pe) {
					early = pe
				} else {
					panic(recovered)
				}
			}
		}()
		actErr = workflow.ExecuteActivity(actCtx, emitActivityName, emitActivityInput{
			RunID: dc.runID,
			Seq:   seq,
			Type:  ev.Type,
			Data:  raw,
		}).Get(actCtx, nil)
	}()
	if early != nil {
		return early
	}
	if actErr != nil {
		coreErr := activityToCore(actErr)
		dc.rt.logStepFailure(dc.tenant, dc.runID, "emit", coreErr)
		if !core.IsRetryable(coreErr) {
			if je := errToJournal(coreErr); je != nil {
				dc.cont.EmitFailures = append(dc.cont.EmitFailures, EmitFailure{
					Call: call,
					Err:  *je,
				})
			}
		}
		return coreErr
	}
	dc.cont.Emitted++
	return nil
}

func (r *Runtime) runEmitActivity(ctx context.Context, in emitActivityInput) error {
	tenant, _ := core.TenantFrom(ctx)
	runID := runIDFromWorkflowID(tenant, workflowIDFromActivity(ctx))
	if runID == "" || in.RunID != runID {
		return activityApplicationError(core.NewError(core.CodeInvalidArgument,
			"temporal: emit run id does not match the scheduling workflow", nil))
	}
	ev := runtime.ProgressEvent{Type: in.Type, Data: append(json.RawMessage(nil), in.Data...)}
	ref := runtime.RunRef{RunID: runID, TenantID: tenant}
	if err := r.outbox.Append(ctx, ref, in.Seq, ev); err != nil {
		return activityApplicationError(err)
	}
	return nil
}
