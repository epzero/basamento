package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/cucumber/godog"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/host"
	"github.com/epzero/basamento/flow/ir"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/runtime/conformance"
	"github.com/epzero/basamento/runtime/local"
	"github.com/epzero/basamento/runtime/temporal"
)

// UpdateGolden makes the conformance steps rewrite the golden records. It is
// set from the test binary's -update flag, never from the gate.
var UpdateGolden bool

const conformanceHoldLimit = 10 * time.Second

// conformanceWorld runs the conformance flows on the runtime the scenario names.
type conformanceWorld struct {
	runtimeName string
	clock       coretestClock

	rt         runtime.Runtime
	localRT    *local.Runtime
	temporalRT *temporal.Runtime
	harness    *conformance.Harness
	ref        runtime.RunRef

	rec       conformance.Record
	replayErr error
}

// coretestClock is the subset of the fake clock a conformance run needs.
type coretestClock interface {
	Now() time.Time
	Advance(time.Duration)
}

func newConformanceWorld() *conformanceWorld { return &conformanceWorld{} }

func conformanceW(ctx context.Context) *conformanceWorld { return world(ctx).Conformance }

func (w *conformanceWorld) build(ctx context.Context) error {
	if w.rt != nil {
		return nil
	}
	w.clock = world(ctx).Core.clock
	if w.runtimeName == "" {
		// RT-002-01 and RT-004-01 are tagged @runtime:temporal and name no
		// runtime step.
		w.runtimeName = "temporal"
	}
	switch w.runtimeName {
	case "local":
		rt := local.New(local.Options{Clock: w.clock, IDs: &coretest.SeqIds{}})
		w.localRT = rt
		w.rt = rt
	case "temporal":
		tw := temporalW(ctx)
		if err := tw.prepare(); err != nil {
			return err
		}
		codec, err := temporal.NewCodec(tw.enc, tw.blobs, 0)
		if err != nil {
			return err
		}
		rt, err := temporal.New(temporal.Options{Clock: w.clock, IDs: &coretest.SeqIds{}, Codec: codec})
		if err != nil {
			return err
		}
		w.temporalRT = rt
		w.rt = rt
	default:
		return fmt.Errorf("no runtime %q was named", w.runtimeName)
	}
	return nil
}

// definitionWorld runs a compiled flow through flow/host on a local runtime, so
// a run keeps the definition hash it started with (REQ-FLOW-005).
type definitionWorld struct {
	mu       sync.Mutex
	catalog  *hostCatalog
	host     *host.Host
	rt       *local.Runtime
	gateSeen chan struct{}
	gateOnce sync.Once
	doneOnce sync.Once
	done     chan struct{}
	out      json.RawMessage
	err      error
	ref      runtime.RunRef
	flowRef  runtime.FlowRef
}

func newDefinitionWorld() *definitionWorld {
	return &definitionWorld{
		catalog:  newHostCatalog(),
		gateSeen: make(chan struct{}),
		done:     make(chan struct{}),
	}
}

func definitionW(ctx context.Context) *definitionWorld { return world(ctx).Definition }

func (w *definitionWorld) runtime(ctx context.Context) *local.Runtime {
	if w.rt != nil {
		return w.rt
	}
	w.host = host.New(w.catalog)
	w.rt = local.New(local.Options{Clock: world(ctx).Core.clock, IDs: &coretest.SeqIds{}})
	if err := w.rt.RegisterFlowHost(w.run); err != nil {
		panic(err)
	}
	return w.rt
}

// gateObservingContext signals the scenario when the run awaits its gate.
type gateObservingContext struct {
	runtime.DurableContext
	world *definitionWorld
}

func (d gateObservingContext) Await(spec runtime.GateSpec) runtime.Future {
	d.world.gateOnce.Do(func() { close(d.world.gateSeen) })
	return d.DurableContext.Await(spec)
}

func (w *definitionWorld) run(dc runtime.DurableContext, ref runtime.FlowRef, in json.RawMessage) (json.RawMessage, error) {
	out, err := w.host.Run(gateObservingContext{DurableContext: dc, world: w}, ref, in)
	w.mu.Lock()
	w.out = append(json.RawMessage(nil), out...)
	w.err = err
	w.mu.Unlock()
	w.doneOnce.Do(func() { close(w.done) })
	return out, err
}

func (w *definitionWorld) register(def *flow.IR) runtime.FlowRef {
	ref := w.catalog.register(def)
	return ref
}

func (w *definitionWorld) waitDone() error {
	select {
	case <-w.done:
		return nil
	case <-time.After(conformanceHoldLimit):
		return fmt.Errorf("the run did not settle within the liveness guard")
	}
}

func reviewFlow(version int, path string) *flow.IR {
	return &flow.IR{
		Name:    "review",
		Version: version,
		State: flow.StateSpec{
			Schema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		},
		Nodes: map[string]flow.Node{
			"approve": {Kind: "gate"},
			"path":    {Kind: "set", Spec: json.RawMessage(`{"path":"` + path + `"}`)},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "approve"}, {From: "approve", To: "path"}},
	}
}

func registerConformance(sc *godog.ScenarioContext) {
	// RT-001-01: the golden records.
	sc.Step(`^conformance flow "([^"]*)" runs with scripted steps$`, func(ctx context.Context, flowName string) error {
		w := conformanceW(ctx)
		if err := w.build(ctx); err != nil {
			return err
		}
		w.harness = conformance.New(flowName, w.clock)
		if err := w.harness.Register(w.rt); err != nil {
			return err
		}
		w.ref = runtime.RunRef{}
		tenantCtx := core.WithTenant(context.Background(), "acme")
		rec, err := w.harness.Run(tenantCtx, w.rt, "run-"+flowName)
		if err != nil {
			return err
		}
		w.rec = rec
		w.ref = w.harness.Ref()
		return nil
	})
	sc.Step(`^the final state and durable events equal the golden record for "([^"]*)"$`, func(ctx context.Context, flowName string) error {
		w := conformanceW(ctx)
		path := filepath.Join("..", "..", "runtime", "conformance", "testdata", flowName+".golden.json")
		if err := conformance.MatchGolden(path, w.rec, UpdateGolden); err != nil {
			return err
		}
		return nil
	})

	// RT-002-01: crash resume.
	sc.Step(`^a run of conformance flow "([^"]*)" has completed step 2 of 4$`, func(ctx context.Context, flowName string) error {
		w := conformanceW(ctx)
		if err := w.build(ctx); err != nil {
			return err
		}
		w.harness = conformance.New(flowName, w.clock)
		w.harness.Block("s3")
		if err := w.harness.Register(w.rt); err != nil {
			return err
		}
		tenantCtx := core.WithTenant(context.Background(), "acme")
		if err := w.harness.Start(tenantCtx, w.rt, "r1"); err != nil {
			return err
		}
		w.ref = w.harness.Ref()
		select {
		case <-w.harness.StepStarted("s3"):
		case <-time.After(conformanceHoldLimit):
			return fmt.Errorf("step 3 never started")
		}
		return nil
	})
	sc.Step(`^the kit process is killed and started again$`, func(ctx context.Context) error {
		w := conformanceW(ctx)
		if w.temporalRT == nil {
			return fmt.Errorf("crash resume needs the temporal runtime")
		}
		tenantCtx := core.WithTenant(context.Background(), "acme")
		if err := w.temporalRT.Kill(tenantCtx, w.ref); err != nil {
			return err
		}
		w.harness.Release("s3")
		return w.temporalRT.Resume(tenantCtx, w.ref)
	})
	sc.Step(`^the run completes$`, func(ctx context.Context) error {
		w := conformanceW(ctx)
		tenantCtx := core.WithTenant(context.Background(), "acme")
		rec, err := w.harness.Wait(tenantCtx, w.rt)
		if err != nil {
			return err
		}
		w.rec = rec
		if rec.State != "completed" {
			return fmt.Errorf("the run is %q, want completed", rec.State)
		}
		return nil
	})
	sc.Step(`^steps 1 and 2 were executed once each$`, func(ctx context.Context) error {
		rec := conformanceW(ctx).rec
		for _, name := range []string{"s1", "s2"} {
			if got := rec.Steps[name]; got != 1 {
				return fmt.Errorf("step %q ran %d times, want 1", name, got)
			}
		}
		return nil
	})

	// RT-004-01: replay.
	sc.Step(`^a recorded history of conformance flow "([^"]*)"$`, func(ctx context.Context, flowName string) error {
		w := conformanceW(ctx)
		if err := w.build(ctx); err != nil {
			return err
		}
		if w.temporalRT == nil {
			return fmt.Errorf("replay needs the temporal runtime")
		}
		w.harness = conformance.New(flowName, w.clock)
		if err := w.harness.Register(w.rt); err != nil {
			return err
		}
		tenantCtx := core.WithTenant(context.Background(), "acme")
		rec, err := w.harness.Run(tenantCtx, w.rt, "r1")
		if err != nil {
			return err
		}
		w.rec = rec
		w.ref = w.harness.Ref()
		return nil
	})
	sc.Step(`^it is replayed against flow code that schedules a different step at event (\d+)$`, func(ctx context.Context, event int) error {
		w := conformanceW(ctx)
		if w.temporalRT == nil {
			return fmt.Errorf("replay needs the temporal runtime")
		}
		w.harness.SetVariant("linear", "linear-mutated")
		tenantCtx := core.WithTenant(context.Background(), "acme")
		w.replayErr = w.temporalRT.Replay(tenantCtx, w.ref)
		return nil
	})
	sc.Step(`^the replay fails with code "([^"]*)" naming event (\d+)$`, func(ctx context.Context, code string, event int) error {
		w := conformanceW(ctx)
		if w.replayErr == nil {
			return fmt.Errorf("the replay succeeded, want %s", code)
		}
		if got := string(core.CodeOf(w.replayErr)); got != code {
			return fmt.Errorf("the replay failed with code %q, want %q", got, code)
		}
		var coded *core.Error
		if !errors.As(w.replayErr, &coded) {
			return fmt.Errorf("the replay error carries no details")
		}
		if got := fmt.Sprint(coded.Details["event"]); got != fmt.Sprint(event) {
			return fmt.Errorf("the replay named event %s, want %d", got, event)
		}
		return nil
	})

	// REQ-FLOW-005: a run keeps the definition it started with.
	sc.Step(`^flow "([^"]*)" version (\d+) waits at gate "([^"]*)"$`, func(ctx context.Context, name string, version int, gate string) error {
		w := definitionW(ctx)
		w.runtime(ctx)
		w.flowRef = w.register(reviewFlow(version, fmt.Sprintf("v%d", version)))
		return nil
	})
	sc.Step(`^a run of "([^"]*)" is waiting at gate "([^"]*)"$`, func(ctx context.Context, name, gate string) error {
		w := definitionW(ctx)
		if name != "review" {
			return fmt.Errorf("no definition %q is scripted", name)
		}
		ref := w.flowRef
		ref.Name = name
		started, err := w.runtime(ctx).Start(core.WithTenant(context.Background(), "acme"), runtime.StartRequest{
			Flow:  ref,
			RunID: "r1",
		})
		if err != nil {
			return err
		}
		w.ref = started
		select {
		case <-w.gateSeen:
			return nil
		case <-time.After(conformanceHoldLimit):
			return fmt.Errorf("the run never reached gate %q", gate)
		}
	})
	sc.Step(`^flow "([^"]*)" is edited to version (\d+) with a different next node$`, func(ctx context.Context, name string, version int) error {
		definitionW(ctx).register(reviewFlow(version, fmt.Sprintf("v%d", version)))
		return nil
	})
	sc.Step(`^the gate "([^"]*)" is resolved$`, func(ctx context.Context, gate string) error {
		w := definitionW(ctx)
		payload := json.RawMessage(`{"approved":true}`)
		return w.runtime(ctx).Resolve(core.WithTenant(context.Background(), "acme"), w.ref, gate, payload)
	})
	sc.Step(`^the run follows version (\d+)$`, func(ctx context.Context, version int) error {
		w := definitionW(ctx)
		if err := w.waitDone(); err != nil {
			return err
		}
		w.mu.Lock()
		out, runErr := w.out, w.err
		w.mu.Unlock()
		if runErr != nil {
			return runErr
		}
		var state struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(out, &state); err != nil {
			return err
		}
		if want := fmt.Sprintf("v%d", version); state.Path != want {
			return fmt.Errorf("the run followed %q, want %q", state.Path, want)
		}
		return nil
	})

	// REQ-FLOW-006: a rejected gate payload leaves the gate open.
	sc.Step(`^the run is still waiting at gate "([^"]*)"$`, func(ctx context.Context, gate string) error {
		w := localStepWorld(ctx)
		state, err := w.stateOf(ctx, "acme", w.current.RunID)
		if err != nil {
			return err
		}
		if state != "running" {
			return fmt.Errorf("the run is %q, want running", state)
		}
		exec := w.currentExec
		exec.mu.Lock()
		at := exec.atGate
		exec.mu.Unlock()
		if at != gate {
			return fmt.Errorf("the run waits at gate %q, want %q", at, gate)
		}
		return nil
	})
}

// settleDefinition is the test-only liveness guard for a definition run.
