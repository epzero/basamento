package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

// holdLimit is a liveness guard for a run that never settles. It is never the
// clock a scenario moves: the fake clock drives flow time, so reaching it means
// the runtime lost a wake-up, not that a wait was long.
const holdLimit = 10 * time.Second

// Clock is the deterministic clock the harness advances to fire a gate timeout.
// coretest.FakeClock satisfies it; production clocks do not need to.
type Clock interface {
	Now() time.Time
	Advance(time.Duration)
}

// Event is one durable event a conformance flow emitted, in emission order.
type Event struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// Record is a run's observable result: the same for every runtime.
type Record struct {
	Flow   string          `json:"flow"`
	State  string          `json:"state"`
	Output json.RawMessage `json:"output"`
	Events []Event         `json:"events"`
	// Steps counts executions per step name. A name executed once appears with
	// 1; ordering is dropped because parallel flows may finish either way.
	Steps map[string]int `json:"steps"`
}

// Harness runs one conformance flow on one runtime.
type Harness struct {
	flow  string
	spec  Spec
	clock Clock

	mu      sync.Mutex
	counts  map[string]int
	events  []Event
	out     json.RawMessage
	err     error
	variant string
	watch   map[string]chan struct{}
	release map[string]chan struct{}

	gateOnce sync.Once
	gateSeen chan struct{}
	doneOnce sync.Once
	done     chan struct{}

	ref runtime.RunRef
}

// New returns a harness for the named conformance flow.
func New(flow string, clock Clock) *Harness {
	return &Harness{
		flow:     flow,
		spec:     Specs[flow],
		clock:    clock,
		counts:   map[string]int{},
		watch:    map[string]chan struct{}{},
		release:  map[string]chan struct{}{},
		gateSeen: make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Register installs the harness's flow host and every conformance step on rt.
// It is the composition root for a conformance run.
func (h *Harness) Register(rt runtime.Runtime) error {
	if err := rt.RegisterFlowHost(h.host); err != nil {
		return err
	}
	for _, name := range StepNames {
		n := name
		if err := rt.RegisterStep(n, h.stepFunc(n), runtime.StepDefaults{}); err != nil {
			return err
		}
	}
	return nil
}

// SetVariant makes the harness run variant flow code under the name flow. The
// replay scenario uses it to schedule a different step at one event (RT-004-01).
func (h *Harness) SetVariant(flow, variant string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.flow == flow {
		h.variant = variant
	}
}

// Block makes the named step wait until Release is called. The scenario blocks
// the step after the crash point so the process can be killed mid-run.
func (h *Harness) Block(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.release[name] == nil {
		h.release[name] = make(chan struct{})
		h.watch[name] = make(chan struct{})
	}
}

// StepStarted closes once the named blocked step begins executing.
func (h *Harness) StepStarted(name string) <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.watch[name]
}

// Release lets a blocked step return.
func (h *Harness) Release(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ch := h.release[name]; ch != nil {
		close(ch)
		delete(h.release, name)
	}
}

// Start starts the run and remembers its reference.
func (h *Harness) Start(ctx context.Context, rt runtime.Runtime, runID string) error {
	ref, err := rt.Start(ctx, runtime.StartRequest{
		Flow:  runtime.FlowRef{Name: h.flow},
		RunID: runID,
	})
	if err != nil {
		return err
	}
	h.ref = ref
	return nil
}

// Run starts the flow, fires a gate timeout when the flow has one, and waits
// for the run to settle.
func (h *Harness) Run(ctx context.Context, rt runtime.Runtime, runID string) (Record, error) {
	if err := h.Start(ctx, rt, runID); err != nil {
		return Record{}, err
	}
	if h.spec.Timeout > 0 {
		select {
		case <-h.gateSeen:
		case <-h.done:
			return Record{}, fail("the flow ended before it awaited its gate")
		case <-ctx.Done():
			return Record{}, ctx.Err()
		}
		h.clock.Advance(h.spec.Timeout + time.Second)
	}
	return h.Wait(ctx, rt)
}

// Wait waits for the run started by Start or Run to settle and returns its
// record.
func (h *Harness) Wait(ctx context.Context, rt runtime.Runtime) (Record, error) {
	select {
	case <-h.done:
	case <-ctx.Done():
		return Record{}, ctx.Err()
	}
	state, err := h.settle(ctx, rt)
	if err != nil {
		return Record{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	rec := Record{
		Flow:   h.flow,
		State:  state,
		Output: append(json.RawMessage(nil), h.out...),
		Events: append([]Event(nil), h.events...),
		Steps:  make(map[string]int, len(h.counts)),
	}
	for name, count := range h.counts {
		rec.Steps[name] = count
	}
	if rec.Output == nil {
		rec.Output = json.RawMessage("null")
	}
	return rec, nil
}

// Ref is the run's reference.
func (h *Harness) Ref() runtime.RunRef { return h.ref }

// settle reads the runtime's final state. The loop is a liveness guard only:
// the run's state changes on the runtime's goroutine, which the harness cannot
// observe directly.
func (h *Harness) settle(ctx context.Context, rt runtime.Runtime) (string, error) {
	deadline := time.After(holdLimit)
	for {
		status, err := rt.Describe(ctx, h.ref)
		if err != nil {
			return "", err
		}
		if status.State != "running" {
			return status.State, nil
		}
		select {
		case <-deadline:
			return "", fail("the run did not settle within the liveness guard")
		default:
			goruntime.Gosched()
		}
	}
}

func (h *Harness) host(dc runtime.DurableContext, ref runtime.FlowRef, in json.RawMessage) (json.RawMessage, error) {
	h.mu.Lock()
	name := ref.Name
	if h.variant != "" && h.flow == ref.Name {
		name = h.variant
	}
	fn := builders[name]
	h.mu.Unlock()
	if fn == nil {
		err := core.NewError(core.CodeNotFound, fmt.Sprintf("conformance: flow %q is not declared", name), map[string]any{"flow": name})
		h.capture(nil, err)
		return nil, err
	}
	out, err := fn(h, dc, in)
	h.capture(out, err)
	return out, err
}

func (h *Harness) capture(out json.RawMessage, err error) {
	h.mu.Lock()
	h.out = append(json.RawMessage(nil), out...)
	h.err = err
	h.mu.Unlock()
	h.doneOnce.Do(func() { close(h.done) })
}

func (h *Harness) stepFunc(name string) runtime.StepFunc {
	return func(ctx context.Context, in json.RawMessage) (json.RawMessage, error) {
		h.mu.Lock()
		h.counts[name]++
		if started := h.watch[name]; started != nil {
			close(started)
			delete(h.watch, name)
		}
		release := h.release[name]
		h.mu.Unlock()
		if release != nil {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, core.NewError(core.CodeCancelled, "conformance: blocked step cancelled", map[string]any{"step": name})
			}
		}
		return stepValue(name), nil
	}
}

// step calls one named step and returns its recorded value.
func (h *Harness) step(dc runtime.DurableContext, name string) (int, error) {
	var out struct {
		Value int `json:"value"`
	}
	if err := dc.Step(name, nil, runtime.StepOptions{}).Get(&out); err != nil {
		return 0, err
	}
	return out.Value, nil
}

// emit writes one durable event and records it in emission order.
func (h *Harness) emit(dc runtime.DurableContext, typ, flow string) error {
	data, err := marshal(map[string]string{"flow": flow})
	if err != nil {
		return err
	}
	if err := dc.Emit(runtime.ProgressEvent{Type: typ, Data: data}); err != nil {
		return err
	}
	h.mu.Lock()
	h.events = append(h.events, Event{Type: typ, Data: data})
	h.mu.Unlock()
	return nil
}

// gateAwaited reports that the flow reached its gate.
func (h *Harness) gateAwaited() {
	h.gateOnce.Do(func() { close(h.gateSeen) })
}

// LoadGolden reads a golden record.
func LoadGolden(path string) (Record, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return Record{}, fmt.Errorf("conformance: golden %s: %w", filepath.Base(path), err)
	}
	return rec, nil
}

// MatchGolden compares got with the golden record at path. With update it
// rewrites the file instead; the caller only passes update from the -update
// flag.
func MatchGolden(path string, got Record, update bool) error {
	if update {
		return writeGolden(path, got)
	}
	want, err := LoadGolden(path)
	if err != nil {
		return err
	}
	gotRaw, err := json.Marshal(got)
	if err != nil {
		return err
	}
	wantRaw, err := json.Marshal(want)
	if err != nil {
		return err
	}
	if !bytes.Equal(gotRaw, wantRaw) {
		return fmt.Errorf("conformance: %s does not match %s:\n  got  %s\n  want %s",
			got.Flow, filepath.Base(path), gotRaw, wantRaw)
	}
	return nil
}

func writeGolden(path string, rec Record) error {
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}
