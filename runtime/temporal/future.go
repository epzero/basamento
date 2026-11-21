package temporal

import (
	"encoding/json"
	"errors"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// stepFuture waits for a Temporal activity result.
type stepFuture struct {
	ctx    workflow.Context
	f      workflow.Future
	rt     *Runtime
	tenant core.TenantID
	wfID   string
	step   string
}

func (f *stepFuture) Get(out any) error {
	var raw json.RawMessage
	var err error
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				f.rt.handleCodecPanic(f.tenant, f.wfID, f.step, recovered)
			}
		}()
		err = f.f.Get(f.ctx, &raw)
	}()
	if err != nil {
		var act *temporal.ActivityError
		if errors.As(err, &act) {
			return activityToCore(err)
		}
		return f.rt.handleCodecError(f.tenant, f.wfID, f.step, err)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if target, ok := out.(*json.RawMessage); ok {
		*target = append(json.RawMessage(nil), raw...)
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (f *stepFuture) Ready() bool {
	return f.f.IsReady()
}

// timerFuture waits for a workflow timer.
type timerFuture struct {
	ctx workflow.Context
	f   workflow.Future
}

func (f *timerFuture) Get(out any) error {
	if err := f.f.Get(f.ctx, nil); err != nil {
		return activityToCore(err)
	}
	return nil
}

func (f *timerFuture) Ready() bool { return f.f.IsReady() }

// immediateFuture is already complete (zero-duration sleep or a cached error).
type immediateFuture struct {
	err error
}

func (f *immediateFuture) Get(any) error { return f.err }
func (f *immediateFuture) Ready() bool   { return true }

func newImmediate(err error) runtime.Future { return &immediateFuture{err: err} }
