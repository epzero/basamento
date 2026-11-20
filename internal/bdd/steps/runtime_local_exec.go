package steps

import (
	"encoding/json"
	"fmt"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

const localSpinBudget = 100_000

func recvWithin[T any](ch <-chan T, budget int, what string) (T, error) {
	var zero T
	for i := 0; i < budget; i++ {
		select {
		case v := <-ch:
			return v, nil
		default:
			goruntime.Gosched()
		}
	}
	return zero, fmt.Errorf("timed out receiving %s after %d yields", what, budget)
}

// localRunKey identifies a run in the step world.
type localRunKey struct {
	tenant core.TenantID
	id     string
}

// localFlow is the scripted flow code a scenario registers under a name. The
// step library plays the composition root for the flow host; the runtime under
// test is the real runtime/local.
type localFlow func(e *localExec, dc runtime.DurableContext, in json.RawMessage) (json.RawMessage, error)

// localExec observes one invocation of one flow, so a step can wait on the
// exact event a scenario talks about (a gate awaited, a timer registered, a
// select answered) without sleeping.
type localExec struct {
	name string

	mu      sync.Mutex
	changed chan struct{}

	done   bool
	result json.RawMessage
	err    error

	waiting bool
	atGate  string
	waitErr error
	timers  int

	selectIndex   int
	selectDone    bool
	selectPayload json.RawMessage

	first, second json.RawMessage
	childOut      json.RawMessage
	stepErr       error

	childCalls []localChildCall
	commands   chan localChildCommand

	watched []watchedTimer
}

type watchedTimer struct {
	future   runtime.Future
	deadline time.Time
}

// watchTimer records a timer future the harness must observe after a clock advance.
func (e *localExec) watchTimer(f runtime.Future, deadline time.Time) {
	e.mu.Lock()
	e.watched = append(e.watched, watchedTimer{future: f, deadline: deadline})
	e.mu.Unlock()
}

// pendingTimersAt returns how many watched timers due at reading are not yet Ready.
func (e *localExec) pendingTimersAt(reading time.Time) int {
	e.mu.Lock()
	watch := append([]watchedTimer(nil), e.watched...)
	e.mu.Unlock()
	n := 0
	for _, t := range watch {
		if !t.deadline.After(reading) && !t.future.Ready() {
			n++
		}
	}
	return n
}

func newLocalExec(name string) *localExec {
	return &localExec{
		name:     name,
		changed:  make(chan struct{}),
		commands: make(chan localChildCommand, 1),
	}
}

func (e *localExec) update(fn func()) {
	e.mu.Lock()
	fn()
	close(e.changed)
	e.changed = make(chan struct{})
	e.mu.Unlock()
}

// wait blocks until ok holds; ok runs under the exec's lock.
func (e *localExec) wait(what string, ok func() bool) error {
	for i := 0; i < localSpinBudget; i++ {
		e.mu.Lock()
		done := ok()
		changed := e.changed
		e.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-changed:
		default:
			goruntime.Gosched()
		}
	}
	return fmt.Errorf("timed out waiting for %s after %d yields", what, localSpinBudget)
}

func (e *localExec) finish(out json.RawMessage, err error) {
	e.update(func() {
		e.done = true
		e.result = append(json.RawMessage(nil), out...)
		e.err = err
	})
}
