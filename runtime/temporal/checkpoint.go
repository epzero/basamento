package temporal

import (
	"time"

	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// Checkpoint may continue the run as new when history length reaches the
// configured event threshold. A cancelled run never continues as new.
// SnapshotRef is unused: continuation state lives in HostInput.Continuation
// rather than an external snapshot.
func (dc *durableContext) Checkpoint(_ runtime.SnapshotRef) error {
	if dc.continuing {
		return cancelledContinuing()
	}
	dc.checkpointCalls++
	if dc.checkpointCalls <= dc.cont.Checkpoints {
		return nil
	}
	if dc.threshold < 0 {
		return nil
	}
	histLen := workflow.GetInfo(dc.wfCtx).GetCurrentHistoryLength()
	if histLen < dc.threshold {
		return nil
	}
	if dc.wfCtx.Err() != nil {
		return core.NewError(core.CodeCancelled, "temporal: the run was cancelled", nil)
	}
	if err := dc.drainInFlight(); err != nil {
		return err
	}
	dc.materializeReadyOutcomes()
	dc.continuing = true
	snap := dc.snapshotContinuation()
	snap.Checkpoints = dc.checkpointCalls
	dc.continueErr = workflow.NewContinueAsNewError(dc.wfCtx, HostWorkflowName, HostInput{
		Flow:         dc.hostInput.Flow,
		Input:        dc.hostInput.Input,
		RunID:        dc.hostInput.RunID,
		Continuation: snap,
	})
	return dc.continueErr
}

func (dc *durableContext) drainInFlight() error {
	err := workflow.Await(dc.wfCtx, func() bool {
		dc.compactInFlight()
		if len(dc.inFlight) > 0 {
			return false
		}
		return workflow.AllHandlersFinished(dc.wfCtx)
	})
	if err != nil {
		return activityToCore(err)
	}
	return nil
}

func (dc *durableContext) finalizeTimerFired() {
	for i := range dc.cont.Timers {
		if i >= len(dc.timerFutures) {
			continue
		}
		f := dc.timerFutures[i]
		if f == nil || !f.IsReady() {
			continue
		}
		if err := f.Get(dc.wfCtx, nil); err != nil {
			continue
		}
		dc.cont.Timers[i].Fired = true
	}
}

func (dc *durableContext) snapshotContinuation() *Continuation {
	dc.finalizeTimerFired()
	out := &Continuation{
		Steps:        map[string]StepOutcome{},
		Children:     map[string]ChildOutcome{},
		Gates:        map[string]GateJournal{},
		Timers:       append([]TimerJournal(nil), dc.cont.Timers...),
		Selects:      append([]int(nil), dc.cont.Selects...),
		Nows:         append([]time.Time(nil), dc.cont.Nows...),
		Emitted:      dc.cont.Emitted,
		EmitFailures: append([]EmitFailure(nil), dc.cont.EmitFailures...),
		Checkpoints:  dc.cont.Checkpoints,
	}
	for k, v := range dc.cont.Steps {
		out.Steps[k] = v
	}
	for k, v := range dc.cont.Children {
		out.Children[k] = v
	}
	for k, v := range dc.cont.Gates {
		out.Gates[k] = v
	}
	return out
}

func cancelledContinuing() error {
	return core.NewError(core.CodeCancelled, "temporal: the run is continuing as new", nil)
}
