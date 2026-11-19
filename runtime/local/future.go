package local

import (
	"encoding/json"
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
func (f *future) complete(result json.RawMessage, err error) bool {
	f.mu.Lock()
	select {
	case <-f.done:
		f.mu.Unlock()
		return false
	default:
	}
	f.result = append(json.RawMessage(nil), result...)
	f.err = err
	f.seq = f.run.seqs.Add(1)
	close(f.done)
	f.mu.Unlock()
	f.run.notify()
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
