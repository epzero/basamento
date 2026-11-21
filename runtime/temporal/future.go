package temporal

import (
	"encoding/json"

	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/runtime"
)

// stepFuture waits for a Temporal activity result.
type stepFuture struct {
	ctx workflow.Context
	f   workflow.Future
}

func (f *stepFuture) Get(out any) error {
	var raw json.RawMessage
	err := f.f.Get(f.ctx, &raw)
	if err != nil {
		return FromActivityError(err)
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
		return FromActivityError(err)
	}
	return nil
}

func (f *timerFuture) Ready() bool { return f.f.IsReady() }

// failedFuture is an already-failed durable operation.
type failedFuture struct {
	err error
}

func (f *failedFuture) Get(any) error { return f.err }
func (f *failedFuture) Ready() bool   { return true }

func newFailed(err error) runtime.Future { return &failedFuture{err: err} }
