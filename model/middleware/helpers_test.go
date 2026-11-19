package middleware

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/structured"
)

// virtualRuntime is a deterministic runtime for unit tests. Sleep advances
// time to its deadline and runs every timer that becomes due before it, so a
// timeout interrupts a provider that sleeps too long and a call needs no
// goroutines.
type virtualRuntime struct {
	mu     sync.Mutex
	now    time.Time
	timers []*virtualTimer
}

type virtualTimer struct {
	when    time.Time
	f       func()
	stopped bool
}

func newVirtualRuntime(at time.Time) *virtualRuntime {
	return &virtualRuntime{now: at.UTC()}
}

func (r *virtualRuntime) Now() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.now
}

func (r *virtualRuntime) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	r.mu.Lock()
	deadline := r.now.Add(d)
	r.mu.Unlock()
	for {
		timer := r.nextDue(deadline)
		if timer == nil {
			r.mu.Lock()
			r.now = deadline
			r.mu.Unlock()
			return ctx.Err()
		}
		r.mu.Lock()
		r.now = timer.when
		timer.stopped = true
		r.mu.Unlock()
		timer.f()
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func (r *virtualRuntime) AfterFunc(d time.Duration, f func()) Timer {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := &virtualTimer{when: r.now.Add(d), f: f}
	r.timers = append(r.timers, t)
	return t
}

func (t *virtualTimer) Stop() {
	// The test runtime is single-goroutine; marking is enough.
	t.stopped = true
}

func (r *virtualRuntime) nextDue(deadline time.Time) *virtualTimer {
	r.mu.Lock()
	defer r.mu.Unlock()
	var next *virtualTimer
	for _, t := range r.timers {
		if t.stopped || t.when.After(deadline) {
			continue
		}
		if next == nil || t.when.Before(next.when) {
			next = t
		}
	}
	return next
}

// fixedRand draws a scripted list of jitter values.
type fixedRand struct {
	draws []int64
	next  int
}

func (r *fixedRand) Int64n(n int64) int64 {
	if r.next >= len(r.draws) {
		return 0
	}
	v := r.draws[r.next]
	r.next++
	if v < 0 {
		v = 0
	}
	if v >= n {
		v = n - 1
	}
	return v
}

type meterCollector struct {
	entries []AttemptMeter
}

func (m *meterCollector) Record(e AttemptMeter) { m.entries = append(m.entries, e) }

func (m *meterCollector) total() (in, out, cost int64) {
	for _, e := range m.entries {
		in += e.Usage.InputTokens
		out += e.Usage.OutputTokens
		cost += e.CostMpts
	}
	return in, out, cost
}

type logCollector struct {
	entries []CallLogEntry
}

func (l *logCollector) Record(e CallLogEntry) { l.entries = append(l.entries, e) }

// scriptedProvider runs a function per attempt, in order. The function may
// sleep on the runtime to model a stream.
type scriptedProvider struct {
	rt      *virtualRuntime
	invoked int
	run     func(ctx context.Context, p *scriptedProvider, a Attempt, emit func(model.Event)) (model.Result, error)
}

func (p *scriptedProvider) Attempt(ctx context.Context, a Attempt, emit func(model.Event)) (model.Result, error) {
	p.invoked++
	return p.run(ctx, p, a, emit)
}

func (p *scriptedProvider) sleep(ctx context.Context, d time.Duration) error {
	return p.rt.Sleep(ctx, d)
}

func assistant(content string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"role": "assistant", "content": content})
	return raw
}

func succeeded(content string, usage *model.Usage) model.Result {
	return model.Result{State: model.StateSucceeded, Message: assistant(content), Usage: usage}
}

func failed(err error) (model.Result, error) { return model.Result{}, err }

func coreErr(code core.Code, retryable bool, after time.Duration) *core.Error {
	return &core.Error{Code: code, Message: string(code), Retryable: retryable, RetryAfter: after}
}

func codeOf(t *testing.T, err error) core.Code {
	t.Helper()
	if err == nil {
		t.Fatal("call succeeded, want an error")
	}
	return core.CodeOf(err)
}

func answerSpec(t *testing.T, supported ...structured.Rung) *structured.Spec {
	t.Helper()
	raw := json.RawMessage(`{"type":"object","properties":{"answer":{"type":"integer"}},"required":["answer"],"additionalProperties":false}`)
	spec, err := structured.NewSpec(raw, supported)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func baseConfig(rt *virtualRuntime, p Provider) Config {
	return Config{
		Model:     "sim-chat",
		ClientRef: "call-1",
		Request:   model.Request{Model: "sim-chat", ClientRef: "call-1"},
		Provider:  p,
		Runtime:   rt,
		Rand:      ZeroRand(),
	}
}
