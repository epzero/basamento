package local

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/epzero/basamento/core"
)

// future is the runtime's Future: a result that becomes readable once and can
// be waited on by any goroutine of the run.
type future struct {
	run *run

	mu     sync.Mutex
	done   chan struct{}
	result json.RawMessage
	err    error
	seq    uint64
}

func newFuture(r *run) *future {
	return &future{run: r, done: make(chan struct{})}
}

// complete records the outcome. The first completion wins; later ones are
// ignored, which is what makes cancellation and a race with a result safe.
// The completion seq is allocated under f.mu immediately before done closes,
// so Select never observes a seq without a matching Ready().
func (f *future) complete(result json.RawMessage, err error) bool {
	f.mu.Lock()
	select {
	case <-f.done:
		f.mu.Unlock()
		return false
	default:
	}
	seq := f.run.seqs.Add(1)
	f.result = append(json.RawMessage(nil), result...)
	f.err = err
	f.seq = seq
	close(f.done)
	f.mu.Unlock()
	f.run.notify()
	return true
}

// completeWithSeq records the outcome using an already-allocated completion
// sequence. notify is false while batching timer completions; the caller
// notifies once after the batch.
func (f *future) completeWithSeq(seq uint64, result json.RawMessage, err error, notify bool) bool {
	f.mu.Lock()
	select {
	case <-f.done:
		f.mu.Unlock()
		return false
	default:
	}
	f.result = append(json.RawMessage(nil), result...)
	f.err = err
	f.seq = seq
	close(f.done)
	f.mu.Unlock()
	if notify {
		f.run.notify()
	}
	return true
}

// completedSeq returns the future's completion order (1-based, per run) and
// whether it has completed at all.
func (f *future) completedSeq() (uint64, bool) {
	select {
	case <-f.done:
	default:
		return 0, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.seq, true
}

// Get waits for the future and decodes its result into out.
func (f *future) Get(out any) error {
	<-f.done
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	if out == nil || len(f.result) == 0 {
		return nil
	}
	return json.Unmarshal(f.result, out)
}

// Ready reports whether the future has completed.
func (f *future) Ready() bool {
	select {
	case <-f.done:
		return true
	default:
		return false
	}
}

// failedFuture returns a future that is already complete with err.
func failedFuture(r *run, err error) *future {
	f := newFuture(r)
	f.complete(nil, err)
	return f
}

func invalidErr(message string, details map[string]any) error {
	return core.NewError(core.CodeInvalidArgument, message, details)
}

// flowPanicErr turns a recovered host panic into a failed run. The recovered
// value is kept as Cause only for logging inside the runtime; Describe, List
// and RunStatus never surface it.
func flowPanicErr(recovered any) error {
	var cause error
	switch x := recovered.(type) {
	case error:
		cause = x
	default:
		cause = fmt.Errorf("%v", x)
	}
	e := core.NewError(core.CodeInternal, "local: the flow host panicked", nil)
	e.Cause = cause
	return e
}

// stepPanicErr turns a recovered step panic into a non-retryable step error. The
// recovered value is kept as Cause only for logging inside the runtime;
// Describe, List and RunStatus never surface it.
func stepPanicErr(step string, recovered any) error {
	var cause error
	switch x := recovered.(type) {
	case error:
		cause = x
	default:
		cause = fmt.Errorf("%v", x)
	}
	e := core.NewError(core.CodeInternal, "local: the step panicked", map[string]any{"step": step})
	e.Cause = cause
	return e
}
