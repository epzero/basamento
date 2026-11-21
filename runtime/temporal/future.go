package temporal

import (
	"encoding/json"

	"go.temporal.io/sdk/workflow"

	"github.com/epzero/basamento/runtime"
)

// stepFuture waits for a Temporal activity result.
type stepFuture struct {
	ctx  workflow.Context
	f    workflow.Future
	done func(json.RawMessage, error)
}

func (f *stepFuture) Get(out any) error {
	var raw json.RawMessage
	err := f.f.Get(f.ctx, &raw)
	if f.done != nil {
		f.done(raw, err)
		f.done = nil
	}
	if err != nil {
		return activityToCore(err)
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

func (f *stepFuture) Ready() bool { return f.f.IsReady() }

func (f *stepFuture) materialize() {
	if f.done == nil || !f.f.IsReady() {
		return
	}
	var raw json.RawMessage
	err := f.f.Get(f.ctx, &raw)
	if f.done != nil {
		f.done(raw, err)
		f.done = nil
	}
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

// gateFuture waits for a gate settlement.
type gateFuture struct {
	ctx workflow.Context
	g   *gateState
}

func (f *gateFuture) Get(out any) error {
	var raw json.RawMessage
	err := f.g.wfFut.Get(f.ctx, &raw)
	if err != nil {
		return activityToCore(err)
	}
	if out == nil {
		return nil
	}
	if target, ok := out.(*json.RawMessage); ok {
		*target = append(json.RawMessage(nil), raw...)
		return nil
	}
	return json.Unmarshal(raw, out)
}

func (f *gateFuture) Ready() bool { return f.g.wfFut.IsReady() }

// childFuture waits for a child workflow result.
type childFuture struct {
	ctx  workflow.Context
	f    workflow.ChildWorkflowFuture
	done func(json.RawMessage, error)
}

func (f *childFuture) Get(out any) error {
	var raw json.RawMessage
	err := f.f.Get(f.ctx, &raw)
	if f.done != nil {
		f.done(raw, childToCore(err))
		f.done = nil
	}
	if err != nil {
		return childToCore(err)
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

func (f *childFuture) Ready() bool { return f.f.IsReady() }

func (f *childFuture) materialize() {
	if f.done == nil || !f.f.IsReady() {
		return
	}
	var raw json.RawMessage
	err := f.f.Get(f.ctx, &raw)
	if f.done != nil {
		f.done(raw, childToCore(err))
		f.done = nil
	}
}

// immediateFuture is already complete (zero-duration sleep or a cached error).
type immediateFuture struct {
	err error
	val json.RawMessage
}

func (f *immediateFuture) Get(out any) error {
	if f.err != nil {
		return f.err
	}
	if out == nil || len(f.val) == 0 {
		return nil
	}
	if target, ok := out.(*json.RawMessage); ok {
		*target = append(json.RawMessage(nil), f.val...)
		return nil
	}
	return json.Unmarshal(f.val, out)
}

func (f *immediateFuture) Ready() bool { return true }

func newImmediate(err error) runtime.Future { return &immediateFuture{err: err} }

func newImmediateResult(val json.RawMessage) runtime.Future {
	return &immediateFuture{val: append(json.RawMessage(nil), val...)}
}
