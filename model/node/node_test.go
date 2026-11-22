package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/model/catalog"
	"github.com/epzero/basamento/runtime"
)

func TestConfigFromSpec(t *testing.T) {
	t.Parallel()
	cfg, err := ConfigFromSpec("generate", json.RawMessage(`{"model":"sim-video","op":"video.generate","output_limit":10}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "generate" || cfg.Model != "sim-video" || cfg.Op != model.CapVideoGenerate || cfg.OutputLimit != 10 {
		t.Fatalf("cfg = %+v", cfg)
	}
	for name, spec := range map[string]string{
		"no model": `{"op":"video.generate"}`,
		"no op":    `{"model":"sim-video"}`,
	} {
		if _, err := ConfigFromSpec("generate", json.RawMessage(spec)); core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("%s: err = %v, want invalid_argument", name, err)
		}
	}
	if _, err := ConfigFromSpec("", json.RawMessage(`{"model":"m","op":"video.generate"}`)); core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("no name: err = %v, want invalid_argument", err)
	}
}

func TestValidateParamsAgainstTheDescriptorSchema(t *testing.T) {
	t.Parallel()
	desc := model.Descriptor{
		ID:     "sim-video",
		Params: json.RawMessage(`{"type":"object","properties":{"duration_s":{"type":"integer","minimum":1,"maximum":10}},"required":["duration_s"]}`),
	}
	if err := validateParams(desc, json.RawMessage(`{"duration_s":5}`)); err != nil {
		t.Fatalf("valid params: %v", err)
	}
	for name, params := range map[string]string{
		"out of range": `{"duration_s":30}`,
		"missing":      `{}`,
		"wrong type":   `{"duration_s":"five"}`,
	} {
		err := validateParams(desc, json.RawMessage(params))
		if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("%s: err = %v, want invalid_argument", name, err)
		}
	}
	if err := validateParams(model.Descriptor{ID: "no-schema"}, nil); err != nil {
		t.Fatalf("a model without a schema rejected its params: %v", err)
	}
}

func TestCheckCapability(t *testing.T) {
	t.Parallel()
	desc := model.Descriptor{ID: "sim-video", Caps: []model.Capability{model.CapVideoGenerate}}
	if err := checkCapability(desc, model.CapVideoGenerate); err != nil {
		t.Fatal(err)
	}
	if err := checkCapability(desc, model.CapImageGenerate); core.CodeOf(err) != core.CodeUnsupported {
		t.Fatalf("err = %v, want unsupported", err)
	}
}

// fakeRegistrar collects the handler's steps so the fake durable context can
// run them synchronously.
type fakeRegistrar struct {
	fns map[string]runtime.StepFunc
}

func newFakeRegistrar() *fakeRegistrar { return &fakeRegistrar{fns: map[string]runtime.StepFunc{}} }

func (r *fakeRegistrar) RegisterStep(name string, fn runtime.StepFunc, _ runtime.StepDefaults) error {
	r.fns[name] = fn
	return nil
}

// fakeDC runs steps synchronously, resolves gates at once and records what the
// handler asked for.
type fakeDC struct {
	ctx         context.Context
	now         time.Time
	seed        uint64
	ids         int
	steps       map[string]runtime.StepFunc
	awaited     []string
	emitted     []string
	gatePayload json.RawMessage
	// recorded replays and records durable step outputs. A non-nil map is a
	// run whose steps were recorded: a present name returns its recorded
	// result instead of running, and a fresh result is stored.
	recorded map[string]json.RawMessage
}

func (d *fakeDC) Context() context.Context { return d.ctx }
func (d *fakeDC) Now() time.Time           { return d.now }
func (d *fakeDC) NewID(kind string) string {
	d.ids++
	return kind + "_" + string(rune('0'+d.ids))
}
func (d *fakeDC) Rand(key string) *rand.Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return rand.New(rand.NewPCG(d.seed, h.Sum64()))
}
func (d *fakeDC) Sleep(time.Duration) runtime.Future {
	return doneFuture{result: json.RawMessage(`null`)}
}
func (d *fakeDC) Await(spec runtime.GateSpec) runtime.Future {
	d.awaited = append(d.awaited, spec.Name)
	if len(d.gatePayload) == 0 {
		return doneFuture{result: json.RawMessage(`{}`)}
	}
	return doneFuture{result: d.gatePayload}
}
func (d *fakeDC) Select(fs ...runtime.Future) (int, error) { return 0, nil }
func (d *fakeDC) Emit(ev runtime.ProgressEvent) error {
	d.emitted = append(d.emitted, ev.Type)
	return nil
}
func (d *fakeDC) Checkpoint(runtime.SnapshotRef) error { return nil }
func (d *fakeDC) Capabilities() runtime.Caps           { return runtime.Caps{} }
func (d *fakeDC) Child(runtime.FlowRef, any, runtime.ChildOptions) runtime.Future {
	return doneFuture{result: json.RawMessage(`null`)}
}
func (d *fakeDC) Step(name string, in any, _ runtime.StepOptions) runtime.Future {
	if raw, ok := d.recorded[name]; ok {
		return doneFuture{result: raw}
	}
	fn, ok := d.steps[name]
	if !ok {
		return doneFuture{err: core.NewError(core.CodeUnsupported, "step not registered", map[string]any{"step": name})}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return doneFuture{err: err}
	}
	out, err := fn(d.ctx, raw)
	if err == nil && d.recorded != nil {
		d.recorded[name] = append(json.RawMessage(nil), out...)
	}
	return doneFuture{result: out, err: err}
}

type doneFuture struct {
	result json.RawMessage
	err    error
}

func (f doneFuture) Get(out any) error {
	if f.err != nil {
		return f.err
	}
	if out == nil || len(f.result) == 0 {
		return nil
	}
	return json.Unmarshal(f.result, out)
}
func (f doneFuture) Ready() bool { return true }

func videoCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c := testCatalog(t)
	err := c.AddDescriptor(model.Descriptor{
		ID:       "sim-video",
		Provider: "sim",
		Caps:     []model.Capability{model.CapVideoGenerate},
		Params:   json.RawMessage(`{"type":"object","properties":{"duration_s":{"type":"integer","minimum":1,"maximum":10}},"required":["duration_s"]}`),
		Protocol: model.ProtocolPoll,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.SetSnapshotPrice("sim-video", catalog.Price{Formula: "params.duration_s * 100"}); err != nil {
		t.Fatal(err)
	}
	return c
}

func runNode(t *testing.T, h *Handler, reg *fakeRegistrar, ctx context.Context, budget *Memory, input string) (Output, error) {
	t.Helper()
	dc := &fakeDC{ctx: ctx, now: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC), steps: reg.fns}
	if budget != nil {
		dc.ctx = WithBudget(ctx, budget)
	}
	return h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(input))
}

func TestHandleReservesPricesAndChargesTheReportedCost(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{Provider: "sim", ID: "v1", ClientRef: req.ClientRef}, nil
	}
	inv.result = model.Result{State: model.StateSucceeded, CostMpts: 480}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	budget := NewMemory(5000)

	out, err := runNode(t, h, reg, t.Context(), budget, `{"params":{"duration_s":5}}`)
	if err != nil {
		t.Fatal(err)
	}
	if out.ReservedMpts != 500 || out.CostMpts != 480 {
		t.Fatalf("output = %+v, want 500 reserved and 480 charged", out)
	}
	if got := budget.Reserved(out.ClientRef); got != 500 {
		t.Fatalf("reserved = %d, want 500", got)
	}
	if got := budget.Charged(out.ClientRef); got != 480 {
		t.Fatalf("charged = %d, want 480", got)
	}
	if !budget.Settled(out.ClientRef) || budget.HeldTotal() != 0 {
		t.Fatalf("budget = held %d settled %v, want closed", budget.HeldTotal(), budget.Settled(out.ClientRef))
	}
	if len(inv.refs) != 1 || inv.refs[0] != out.ClientRef {
		t.Fatalf("provider saw refs %v, want %q", inv.refs, out.ClientRef)
	}
}

func TestHandleRefusesAnInvocationOverBudget(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})

	_, err := runNode(t, h, reg, t.Context(), NewMemory(300), `{"params":{"duration_s":5}}`)
	if core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("err = %v, want budget_exceeded", err)
	}
	if inv.starts != 0 {
		t.Fatalf("starts = %d, want 0", inv.starts)
	}
}

func TestHandleValidatesBeforeReservingOrStarting(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	budget := NewMemory(5000)

	_, err := runNode(t, h, reg, t.Context(), budget, `{"params":{"duration_s":30}}`)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if inv.starts != 0 {
		t.Fatalf("starts = %d, want 0", inv.starts)
	}
	if budget.HeldTotal() != 0 {
		t.Fatalf("held = %d, want no reservation", budget.HeldTotal())
	}
}

func TestHandleOpensTheAmbiguousStartGate(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{}, retryable()
	}
	inv.lookup = func(string) (*model.Handle, error) {
		return nil, core.NewError(core.CodeUnsupported, "no lookup", nil)
	}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	dc := &fakeDC{ctx: t.Context(), steps: reg.fns}
	budget := NewMemory(5000)
	dc.ctx = WithBudget(t.Context(), budget)

	_, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if core.CodeOf(err) != core.CodeAmbiguousStart {
		t.Fatalf("err = %v, want ambiguous_start", err)
	}
	// The gate name carries this invocation's ref: parallel map items must
	// not collide on one gate.
	if len(dc.awaited) != 1 || dc.awaited[0] != AmbiguousStartGate+"."+inv.refs[0] {
		t.Fatalf("awaited = %v, want [%s.%s]", dc.awaited, AmbiguousStartGate, inv.refs[0])
	}
	if inv.starts != 1 {
		t.Fatalf("starts = %d, want 1: a blind restart would charge twice", inv.starts)
	}
	// The start may have reached the provider and the lookup could not say, so
	// the actual charge is unknown: fail closed at the reserved estimate.
	ref := inv.refs[0]
	if got := budget.Charged(ref); got != 500 {
		t.Fatalf("charged = %d, want the reserved estimate 500", got)
	}
	if !budget.Settled(ref) || budget.HeldTotal() != 0 {
		t.Fatalf("budget = held %d settled %v, want closed", budget.HeldTotal(), budget.Settled(ref))
	}
}

func TestHandleFailsAnInvalidParameterBeforeTheStart(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})

	_, err := runNode(t, h, reg, t.Context(), nil, `{"params":{}}`)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if inv.starts != 0 {
		t.Fatalf("starts = %d, want 0", inv.starts)
	}
}

func TestHandleChargesTheFormulaWhenTheProviderReportsUsage(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	inv.result = model.Result{State: model.StateSucceeded, Usage: &model.Usage{InputTokens: 1000, OutputTokens: 500}}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	budget := NewMemory(5000)

	out, err := runNode(t, h, reg, t.Context(), budget, `{"params":{"duration_s":5}}`)
	if err != nil {
		t.Fatal(err)
	}
	if out.CostMpts != 500 {
		t.Fatalf("cost = %d, want the formula's 500", out.CostMpts)
	}
	if got := budget.Charged(out.ClientRef); got != 500 {
		t.Fatalf("charged = %d, want 500", got)
	}
}

func TestHandleChargesTheEstimateWhenAResultHasNoUsage(t *testing.T) {
	t.Parallel()
	for _, state := range []model.State{model.StateSucceeded, model.StateFailed} {
		t.Run(string(state), func(t *testing.T) {
			t.Parallel()
			inv := newScriptInvoker()
			inv.start = func(_ int, req model.Request) (model.Handle, error) {
				return model.Handle{ClientRef: req.ClientRef}, nil
			}
			// Poll and video results often report neither a cost nor a usage.
			inv.result = model.Result{State: state}
			reg := newFakeRegistrar()
			h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
			budget := NewMemory(5000)

			out, _ := runNode(t, h, reg, t.Context(), budget, `{"params":{"duration_s":5}}`)
			ref := inv.refs[0]
			if got := budget.Charged(ref); got != 500 {
				t.Fatalf("charged = %d, want the reserved estimate 500", got)
			}
			if out.CostMpts != 500 {
				t.Fatalf("cost = %d, want the reserved estimate 500", out.CostMpts)
			}
			if !budget.Settled(ref) || budget.HeldTotal() != 0 {
				t.Fatalf("budget = held %d settled %v, want closed", budget.HeldTotal(), budget.Settled(ref))
			}
		})
	}
}

func TestHandleReleasesACancelledInvocationThatReportsNoCost(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	inv.result = model.Result{State: model.StateCancelled}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	budget := NewMemory(5000)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dc := &fakeDC{
		ctx:   WithBudget(ctx, budget),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}

	_, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("err = %v, want cancelled", err)
	}
	ref := inv.refs[0]
	if got := budget.Charged(ref); got != 0 {
		t.Fatalf("charged = %d, want 0", got)
	}
	if !budget.Settled(ref) || budget.HeldTotal() != 0 {
		t.Fatalf("budget = held %d settled %v, want released", budget.HeldTotal(), budget.Settled(ref))
	}
}

func TestHandleDoesNotReemitProviderDeltas(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	inv.events = []model.Event{
		{Seq: 1, Kind: model.EventProgress, Progress: 0.1},
		{Seq: 2, Kind: model.EventProgress, Progress: 1},
	}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	dc := &fakeDC{ctx: WithBudget(t.Context(), NewMemory(5000)), steps: reg.fns}

	if _, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`)); err != nil {
		t.Fatal(err)
	}
	// Deltas are bus-only (streaming.md §2, §4); publishing them is WP-L6-02,
	// so the durable node must not replay them as model.delta events.
	if len(dc.emitted) != 0 {
		t.Fatalf("emitted = %v, want no re-emitted deltas", dc.emitted)
	}
}

// tokenCatalog is a chat model priced only by its token rates.
func tokenCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c := testCatalog(t)
	err := c.AddDescriptor(model.Descriptor{
		ID:       "sim-chat",
		Provider: "sim",
		Caps:     []model.Capability{model.CapChat},
		Protocol: model.ProtocolStream,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = c.SetSnapshotPrice("sim-chat", catalog.Price{Rules: []catalog.Rule{{
		Rates: catalog.Rates{"input": 1000, "output": 8000},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// unpricedCatalog is a chat model with no price in any layer.
func unpricedCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c := testCatalog(t)
	err := c.AddDescriptor(model.Descriptor{
		ID:       "sim-free",
		Provider: "sim",
		Caps:     []model.Capability{model.CapChat},
		Protocol: model.ProtocolStream,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func chatConfig(modelID string, inputTokens int64) Config {
	return Config{Name: "chat", Model: modelID, Op: model.CapChat, InputTokens: inputTokens}
}

func TestHandleFailsAPricedCallWithoutARunBudget(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	dc := &fakeDC{ctx: t.Context(), now: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC), steps: reg.fns}

	_, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("err = %v, want internal: a priced call needs a run budget", err)
	}
	if inv.starts != 0 {
		t.Fatalf("starts = %d, want 0", inv.starts)
	}
}

func TestHandleFailsAPricedCallWithoutDeclaredUsage(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: tokenCatalog(t), Steps: reg})
	budget := NewMemory(5000)
	dc := &fakeDC{ctx: WithBudget(t.Context(), budget), now: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC), steps: reg.fns}

	_, err := h.Handle(dc, chatConfig("sim-chat", 0), json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if inv.starts != 0 || budget.HeldTotal() != 0 {
		t.Fatalf("starts = %d held = %d, want no start and no reservation", inv.starts, budget.HeldTotal())
	}
}

func TestHandleRunsAnUnpricedModelUnmetered(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: unpricedCatalog(t), Steps: reg})
	dc := &fakeDC{ctx: t.Context(), now: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC), steps: reg.fns}

	out, err := h.Handle(dc, chatConfig("sim-free", 0), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if inv.starts != 1 || out.CostMpts != 0 {
		t.Fatalf("starts = %d cost = %d, want an unmetered run", inv.starts, out.CostMpts)
	}
}

func TestHandlePricesAtTheRunClock(t *testing.T) {
	t.Parallel()
	c := testCatalog(t)
	if err := c.AddDescriptor(model.Descriptor{
		ID:       "sim-video",
		Provider: "sim",
		Caps:     []model.Capability{model.CapChat},
		Protocol: model.ProtocolPoll,
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSnapshotPrice("sim-video", catalog.Price{Rules: []catalog.Rule{
		{When: "at.getHours('UTC') >= 17", Rates: catalog.Rates{"input": 140, "output": 550}},
		{Rates: catalog.Rates{"input": 280, "output": 1100}},
	}}); err != nil {
		t.Fatal(err)
	}
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: c, Steps: reg})
	// The fake run clock is in the off-peak window; the catalog's own clock
	// (the system clock) is not consulted.
	dc := &fakeDC{
		ctx:   WithBudget(t.Context(), NewMemory(5000)),
		now:   time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}

	out, err := h.Handle(dc, chatConfig("sim-video", 1_000_000), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if out.ReservedMpts != 140 {
		t.Fatalf("reserved = %d, want the off-peak estimate 140", out.ReservedMpts)
	}
}

func TestHandleReplaySettlesByThePinnedPrice(t *testing.T) {
	t.Parallel()
	cat := videoCatalog(t)
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	// A usage forces settlement through the price formula rather than the
	// no-usage estimate path.
	inv.result = model.Result{State: model.StateSucceeded, Usage: &model.Usage{}}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: cat, Steps: reg})
	recorded := map[string]json.RawMessage{}
	run := func(budget *Memory) Output {
		dc := &fakeDC{
			ctx:      WithBudget(t.Context(), budget),
			now:      time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
			steps:    reg.fns,
			recorded: recorded,
		}
		out, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	firstBudget := NewMemory(50000)
	first := run(firstBudget)
	if first.ReservedMpts != 500 || first.CostMpts != 500 {
		t.Fatalf("first run = %+v, want the entry's 500 reserved and charged", first)
	}

	// The deployment changes after the call started. The replay must settle
	// by the recorded quote, not price the same params again.
	if err := cat.SetDeploymentPrice("sim-video", catalog.Price{Formula: "params.duration_s * 900"}); err != nil {
		t.Fatal(err)
	}
	replayBudget := NewMemory(50000)
	replayed := run(replayBudget)
	if replayed.ReservedMpts != 500 || replayed.CostMpts != 500 {
		t.Fatalf("replay = %+v, want the pinned entry's 500, not the new entry's 4500", replayed)
	}
	if got := replayBudget.Charged(replayed.ClientRef); got != 500 {
		t.Fatalf("charged = %d, want the pinned entry's 500", got)
	}
	if len(inv.refs) != 1 {
		t.Fatalf("provider starts = %d, want 1: the replay reuses the recorded start", len(inv.refs))
	}
}

func TestHandleChargesTheEstimateWhenTheInvocationFailsAfterStarting(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	inv.eventsErr = core.NewError(core.CodeUnavailable, "the event stream broke", nil)
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	budget := NewMemory(5000)

	_, err := runNode(t, h, reg, t.Context(), budget, `{"params":{"duration_s":5}}`)
	if core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("err = %v, want unavailable", err)
	}
	ref := inv.refs[0]
	if got := budget.Charged(ref); got != 500 {
		t.Fatalf("charged = %d, want the reserved estimate 500", got)
	}
	if !budget.Settled(ref) || budget.HeldTotal() != 0 {
		t.Fatalf("budget = held %d settled %v, want closed", budget.HeldTotal(), budget.Settled(ref))
	}
}

func TestHandleChargesTheEstimateWhenTheActualCostCannotBePriced(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	// Cached tokens above input tokens fail settlement, so the actual charge
	// is unknown after the provider already ran.
	inv.result = model.Result{State: model.StateSucceeded, Usage: &model.Usage{InputTokens: 1000, CachedTokens: 2000}}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: tokenCatalog(t), Steps: reg})
	budget := NewMemory(5000)
	dc := &fakeDC{ctx: WithBudget(t.Context(), budget), now: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC), steps: reg.fns}

	_, err := h.Handle(dc, chatConfig("sim-chat", 1000), json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	ref := inv.refs[0]
	if got := budget.Charged(ref); got != 1 {
		t.Fatalf("charged = %d, want the reserved estimate 1", got)
	}
	if !budget.Settled(ref) {
		t.Fatal("the reservation is still open")
	}
}

// failingBudget refuses every settlement.
type failingBudget struct{ err error }

func (b failingBudget) Reserve(string, int64) error { return nil }
func (b failingBudget) Settle(string, int64) error  { return b.err }
func (b failingBudget) Remaining() (int64, bool)    { return 0, false }

func TestHandleReturnsSettlementErrors(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	inv.result = model.Result{State: model.StateSucceeded, CostMpts: 480}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	settleErr := errors.New("the budget is unavailable")
	dc := &fakeDC{
		ctx:   WithBudget(t.Context(), failingBudget{err: settleErr}),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}

	out, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if !errors.Is(err, settleErr) {
		t.Fatalf("err = %v, want the settlement error", err)
	}
	if out.CostMpts != 0 {
		t.Fatalf("cost = %d, want no reported charge after a failed settlement", out.CostMpts)
	}
}

// flakyBudget fails the first failed settlements before delegating to a
// Memory budget, and records every amount it was asked to settle.
type flakyBudget struct {
	*Memory
	fails   int
	amounts []int64
}

func (b *flakyBudget) Settle(key string, actual int64) error {
	b.amounts = append(b.amounts, actual)
	if b.fails > 0 {
		b.fails--
		return errors.New("the budget is unavailable")
	}
	return b.Memory.Settle(key, actual)
}

func TestHandleRetriesAFailedSettlementWithTheActualCharge(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	inv.result = model.Result{State: model.StateSucceeded, CostMpts: 480}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	budget := &flakyBudget{Memory: NewMemory(5000), fails: 1}
	dc := &fakeDC{
		ctx:   WithBudget(t.Context(), budget),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}

	_, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if err == nil {
		t.Fatal("the failed settlement was not reported")
	}
	ref := inv.refs[0]
	// The retry must settle the known actual charge, not first-win the
	// reserved estimate (500), which would make the undercharge permanent.
	if got := budget.Charged(ref); got != 480 {
		t.Fatalf("charged = %d, want the actual 480", got)
	}
	if len(budget.amounts) != 2 || budget.amounts[1] != 480 {
		t.Fatalf("settle amounts = %v, want [480 480]", budget.amounts)
	}
}

func TestHandleSettlesTheFinalStateWhenCancelFails(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	inv.cancel = func(model.Handle) error {
		return core.NewError(core.CodeUnavailable, "the provider did not answer the cancel", nil)
	}
	inv.resultFn = func() (model.Result, error) {
		return model.Result{State: model.StateCancelled, CostMpts: 200}, nil
	}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	budget := NewMemory(5000)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dc := &fakeDC{
		ctx:   WithBudget(ctx, budget),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}

	_, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("err = %v, want cancelled", err)
	}
	// A failed cancel must not skip the final result: the provider's final
	// state (200) settles the reservation, not the reserved estimate (500).
	if got := budget.Charged(inv.refs[0]); got != 200 {
		t.Fatalf("charged = %d, want the provider's final 200", got)
	}
	if inv.resultCtxErr != nil {
		t.Fatalf("result read with %v, want a detached context", inv.resultCtxErr)
	}
}

func TestHandleFailsClosedWhenACancelledStartCannotBeResolved(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	ctx, cancel := context.WithCancel(t.Context())
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		// The run is cancelled while the attempt is in flight and the lookup
		// cannot find the invocation.
		cancel()
		return model.Handle{}, retryable()
	}
	inv.lookup = func(string) (*model.Handle, error) {
		return nil, core.NewError(core.CodeUnsupported, "no lookup", nil)
	}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	budget := NewMemory(5000)
	dc := &fakeDC{ctx: WithBudget(ctx, budget), now: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC), steps: reg.fns}

	_, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if core.CodeOf(err) != core.CodeAmbiguousStart {
		t.Fatalf("err = %v, want ambiguous_start", err)
	}
	ref := inv.refs[0]
	if got := budget.Charged(ref); got != 500 {
		t.Fatalf("charged = %d, want the reserved estimate 500: the start may have landed", got)
	}
	if !budget.Settled(ref) {
		t.Fatal("the reservation is still open")
	}
}

func TestClientRefsAreScopedToTheRun(t *testing.T) {
	t.Parallel()
	newRun := func(seed uint64) (ref, providerRef string) {
		inv := newScriptInvoker()
		inv.start = func(_ int, req model.Request) (model.Handle, error) {
			return model.Handle{ClientRef: req.ClientRef}, nil
		}
		reg := newFakeRegistrar()
		h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
		dc := &fakeDC{
			ctx:   WithBudget(t.Context(), NewMemory(5000)),
			now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
			seed:  seed,
			steps: reg.fns,
		}
		out, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
		if err != nil {
			t.Fatal(err)
		}
		return out.ClientRef, inv.refs[0]
	}

	first, providerFirst := newRun(1)
	replayed, _ := newRun(1)
	if first != replayed || providerFirst != first {
		t.Fatalf("replay refs differ: %q and %q", first, replayed)
	}
	other, _ := newRun(2)
	if other == first {
		t.Fatalf("two runs share the client ref %q", first)
	}
}

func TestHandleRejectsAGateHandleFromAnotherInvocation(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, _ model.Request) (model.Handle, error) {
		return model.Handle{}, retryable()
	}
	inv.lookup = func(string) (*model.Handle, error) {
		return nil, core.NewError(core.CodeUnsupported, "no lookup", nil)
	}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	dc := &fakeDC{
		ctx:   WithBudget(t.Context(), NewMemory(5000)),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
		// One gate serves the whole run; this handle belongs to another node.
		gatePayload: json.RawMessage(`{"handle":{"id":"v-other","client_ref":"mn_other"}}`),
	}

	_, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if core.CodeOf(err) != core.CodeAmbiguousStart {
		t.Fatalf("err = %v, want ambiguous_start", err)
	}
	var ce *core.Error
	if !errors.As(err, &ce) || ce.Details["cause"] != "handle_ref_mismatch" {
		t.Fatalf("err = %v, want a handle_ref_mismatch detail", err)
	}
	// The starter's own lookup (1) was the ambiguous resolution attempt; the
	// mismatched gate handle must not trigger another one or a follow.
	if inv.lookups != 1 {
		t.Fatalf("lookups = %d, want only the starter's 1", inv.lookups)
	}
}

func TestHandleFailsSuccessfulOutputsWithoutABlobStore(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	inv.result = model.Result{State: model.StateSucceeded, Outputs: []model.Output{{URL: "https://vendor.test/a.png"}}}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})

	_, err := runNode(t, h, reg, t.Context(), NewMemory(5000), `{"params":{"duration_s":5}}`)
	if core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("err = %v, want internal: outputs need a blob store", err)
	}
}

func TestHandleNeverLetsIngestMaskCancellation(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	// Outputs with no blob store would fail ingestion; cancellation must win.
	inv.result = model.Result{State: model.StateSucceeded, Outputs: []model.Output{{URL: "https://vendor.test/a.png"}}}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dc := &fakeDC{
		ctx:   WithBudget(ctx, NewMemory(5000)),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}

	_, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("err = %v, want cancelled", err)
	}
}

func TestHandleDoesNotIngestAFailedInvocation(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	inv.result = model.Result{
		State:   model.StateFailed,
		Error:   json.RawMessage(`{"code":"vendor_error","message":"nope"}`),
		Outputs: []model.Output{{URL: "https://vendor.test/a.png"}},
	}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})

	_, err := runNode(t, h, reg, t.Context(), NewMemory(5000), `{"params":{"duration_s":5}}`)
	if core.CodeOf(err) != core.CodeVendor {
		t.Fatalf("err = %v, want the provider's failure, not an ingest error", err)
	}
}

func TestRegisterBoundsTheStepMemo(t *testing.T) {
	t.Parallel()
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: newScriptInvoker(), Catalog: videoCatalog(t), Steps: reg})
	fn := func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }
	for i := 0; i <= maxRegisteredSteps+1; i++ {
		if err := h.register(fmt.Sprintf("model.step-%d", i), fn, stepDefaults()); err != nil {
			t.Fatal(err)
		}
	}
	h.mu.Lock()
	size := len(h.registered)
	h.mu.Unlock()
	if size > maxRegisteredSteps {
		t.Fatalf("registered memo holds %d names, want at most %d", size, maxRegisteredSteps)
	}
}

// conflictRegistrar reports every registration as a conflict, like a runtime
// whose step names were registered from the flow definition first.
type conflictRegistrar struct{ calls int }

func (r *conflictRegistrar) RegisterStep(string, runtime.StepFunc, runtime.StepDefaults) error {
	r.calls++
	return core.NewError(core.CodeConflict, "already registered", nil)
}

func TestRegisterTreatsAConflictAsSuccess(t *testing.T) {
	t.Parallel()
	reg := &conflictRegistrar{}
	h, err := New(Deps{Invoker: newScriptInvoker(), Catalog: videoCatalog(t), Steps: reg})
	if err != nil {
		t.Fatal(err)
	}
	fn := func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, nil }
	if err := h.register("model.generate.start.ref", fn, stepDefaults()); err != nil {
		t.Fatalf("register = %v, want conflict tolerated", err)
	}
	if reg.calls != 1 {
		t.Fatalf("registrations = %d, want 1", reg.calls)
	}
}

func TestHandleIngestsOutputsBeforeSettlingTheReservation(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	inv.result = model.Result{
		State:    model.StateSucceeded,
		CostMpts: 480,
		Outputs:  []model.Output{{URL: "https://vendor.test/a.png", Media: "image", Mime: "image/png"}},
	}
	reg := newFakeRegistrar()
	budget := NewMemory(5000)
	heldDuringIngest := int64(-1)
	h := testHandler(t, Deps{
		Invoker: inv,
		Catalog: videoCatalog(t),
		Steps:   reg,
		Blobs:   newMemBlob(),
		Fetch: func(context.Context, model.Output) ([]byte, string, error) {
			// ADR-0006 orders ingest before settle: the reservation is still
			// held while the output is being stored.
			heldDuringIngest = budget.HeldTotal()
			return []byte("image"), "image/png", nil
		},
	})
	dc := &fakeDC{
		ctx:   WithBudget(t.Context(), budget),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}

	out, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if err != nil {
		t.Fatal(err)
	}
	if heldDuringIngest != 500 {
		t.Fatalf("held during ingest = %d, want the open reservation 500", heldDuringIngest)
	}
	if len(out.Assets) != 1 {
		t.Fatalf("assets = %d, want 1", len(out.Assets))
	}
	if got := budget.Charged(out.ClientRef); got != 480 {
		t.Fatalf("charged = %d, want the actual 480 after ingest", got)
	}
}

func TestHandleChargesTheActualWhenAnIngestFails(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	inv.result = model.Result{
		State:    model.StateSucceeded,
		CostMpts: 480,
		Outputs:  []model.Output{{URL: "https://vendor.test/a.png", Media: "image"}},
	}
	reg := newFakeRegistrar()
	budget := NewMemory(5000)
	h := testHandler(t, Deps{
		Invoker: inv,
		Catalog: videoCatalog(t),
		Steps:   reg,
		Blobs:   newMemBlob(),
		Fetch: func(context.Context, model.Output) ([]byte, string, error) {
			return nil, "", errors.New("the vendor is gone")
		},
	})
	dc := &fakeDC{
		ctx:   WithBudget(t.Context(), budget),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}

	_, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if core.CodeOf(err) != core.CodeVendor {
		t.Fatalf("err = %v, want vendor", err)
	}
	// The invocation ran and its cost is known: a failed ingest must not let
	// the deferred settlement fall back to the estimate.
	if got := budget.Charged(inv.refs[0]); got != 480 {
		t.Fatalf("charged = %d, want the pinned actual 480", got)
	}
	if !budget.Settled(inv.refs[0]) || budget.HeldTotal() != 0 {
		t.Fatalf("budget = held %d settled %v, want closed", budget.HeldTotal(), budget.Settled(inv.refs[0]))
	}
}

func TestHandleSettlesByTheTerminalStateWhenTheRunIsCancelled(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	// The provider finished before the cancellation reached it, and reports no
	// usage: the run's cancel bit must not release the reservation for zero.
	inv.result = model.Result{State: model.StateSucceeded}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	budget := NewMemory(5000)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dc := &fakeDC{
		ctx:   WithBudget(ctx, budget),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}

	_, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("err = %v, want cancelled", err)
	}
	if got := budget.Charged(inv.refs[0]); got != 500 {
		t.Fatalf("charged = %d, want the reserved estimate 500: the provider succeeded", got)
	}
}

// endedAfterStartDC mirrors the local runtime's step gate: a step that has not
// started when the run is cancelled completes cancelled without running.
type endedAfterStartDC struct{ *fakeDC }

func (d endedAfterStartDC) Step(name string, in any, o runtime.StepOptions) runtime.Future {
	if d.ctx.Err() != nil {
		return doneFuture{err: core.NewError(core.CodeCancelled, "step not started: run cancelled", nil)}
	}
	return d.fakeDC.Step(name, in, o)
}

// TestHandleCancelsAnInvocationWhenTheRunEndsBeforeTheFollowStep covers the
// window between the provider accepting the invocation and the node starting
// to follow it: the runtime ends the cancelled run without running the follow
// step, and the invocation, still running at the provider, must be cancelled
// and settled by the state the provider reports (REQ-MODEL-009).
func TestHandleCancelsAnInvocationWhenTheRunEndsBeforeTheFollowStep(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		// The run is cancelled as the provider accepts the invocation.
		cancel()
		return model.Handle{Provider: "sim", ID: "v1", ClientRef: req.ClientRef}, nil
	}
	inv.result = model.Result{State: model.StateCancelled, CostMpts: 200}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	budget := NewMemory(5000)
	dc := endedAfterStartDC{fakeDC: &fakeDC{
		ctx:   WithBudget(ctx, budget),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}}

	_, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("err = %v, want cancelled", err)
	}
	if inv.cancels != 1 {
		t.Fatalf("the provider saw %d cancels, want 1: the invocation was left running", inv.cancels)
	}
	if inv.cancelCtxErr != nil {
		t.Fatalf("the cancel ran on a context the cancellation reached: %v", inv.cancelCtxErr)
	}
	ref := inv.refs[0]
	if got := budget.Charged(ref); got != 200 {
		t.Fatalf("charged = %d, want the provider's 200", got)
	}
	if !budget.Settled(ref) || budget.HeldTotal() != 0 {
		t.Fatalf("budget = held %d settled %v, want closed", budget.HeldTotal(), budget.Settled(ref))
	}
}

// cancelledFollowDC mirrors the Temporal runtime's cancellation shape: its
// Context() stays live after the workflow is cancelled
// (runtime/temporal/durable.go returns context.Background() plus the tenant),
// so a run that ends before the follow step surfaces only as that step's
// cancelled error.
type cancelledFollowDC struct{ *fakeDC }

func (d cancelledFollowDC) Step(name string, in any, o runtime.StepOptions) runtime.Future {
	if _, ok := in.(followIn); ok {
		return doneFuture{err: core.NewError(core.CodeCancelled, "temporal: activity cancelled", nil)}
	}
	return d.fakeDC.Step(name, in, o)
}

// TestHandleCancelsAnInvocationWhenTheStepSaysTheRunWasCancelled covers the
// same window as the local test above on a runtime whose context stays live
// after the run is cancelled: the step's cancelled code is the only signal,
// and the invocation must still be cancelled and settled by the state the
// provider reports (REQ-MODEL-009).
func TestHandleCancelsAnInvocationWhenTheStepSaysTheRunWasCancelled(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{Provider: "sim", ID: "v1", ClientRef: req.ClientRef}, nil
	}
	inv.result = model.Result{State: model.StateCancelled, CostMpts: 200}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	budget := NewMemory(5000)
	// The context stays live, as on Temporal: there is no cancel bit to read.
	dc := cancelledFollowDC{fakeDC: &fakeDC{
		ctx:   WithBudget(context.Background(), budget),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}}

	_, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("err = %v, want cancelled", err)
	}
	if inv.cancels != 1 {
		t.Fatalf("the provider saw %d cancels, want 1: the invocation was left running", inv.cancels)
	}
	ref := inv.refs[0]
	if got := budget.Charged(ref); got != 200 {
		t.Fatalf("charged = %d, want the provider's 200", got)
	}
	if !budget.Settled(ref) || budget.HeldTotal() != 0 {
		t.Fatalf("budget = held %d settled %v, want closed", budget.HeldTotal(), budget.Settled(ref))
	}
}

// failedFollowDC fails the follow step with an unrelated error while the
// context stays live, as a provider or runtime failure would.
type failedFollowDC struct{ *fakeDC }

func (d failedFollowDC) Step(name string, in any, o runtime.StepOptions) runtime.Future {
	if _, ok := in.(followIn); ok {
		return doneFuture{err: core.NewError(core.CodeUnavailable, "the provider dropped the stream", nil)}
	}
	return d.fakeDC.Step(name, in, o)
}

// TestHandleDoesNotCancelOnAnUnrelatedFollowError guards the cancel gate: only
// a cancelled run cancels the invocation, and any other follow failure is
// reported as it is.
func TestHandleDoesNotCancelOnAnUnrelatedFollowError(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{Provider: "sim", ID: "v1", ClientRef: req.ClientRef}, nil
	}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	budget := NewMemory(5000)
	dc := failedFollowDC{fakeDC: &fakeDC{
		ctx:   WithBudget(context.Background(), budget),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}}

	_, err := h.Handle(dc, Config{Name: "generate", Model: "sim-video", Op: model.CapVideoGenerate}, json.RawMessage(`{"params":{"duration_s":5}}`))
	if core.CodeOf(err) != core.CodeUnavailable {
		t.Fatalf("err = %v, want the follow error", err)
	}
	if inv.cancels != 0 {
		t.Fatalf("the provider saw %d cancels, want none: the run was not cancelled", inv.cancels)
	}
}

func TestHandleEstimateDoesNotTrustAnUnderstatedInputCount(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	inv.result = model.Result{State: model.StateSucceeded, Usage: &model.Usage{InputTokens: 1000}}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: tokenCatalog(t), Steps: reg})
	budget := NewMemory(100000)
	dc := &fakeDC{
		ctx:   WithBudget(t.Context(), budget),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}

	// The flow input declares one token, but the node's spec counts a million:
	// the input may raise the estimate, never lower it.
	cfg := Config{Name: "chat", Model: "sim-chat", Op: model.CapChat, InputTokens: 1_000_000}
	if _, err := h.Handle(dc, cfg, json.RawMessage(`{"input_tokens":1}`)); err != nil {
		t.Fatal(err)
	}
	if got := budget.Reserved(inv.refs[0]); got != 1000 {
		t.Fatalf("reserved = %d, want the configured count's 1000", got)
	}
}

func TestHandleEstimateCountsTheRequestsMaxTokens(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	inv.result = model.Result{State: model.StateSucceeded, Usage: &model.Usage{InputTokens: 1000}}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: tokenCatalog(t), Steps: reg})
	budget := NewMemory(100000)
	dc := &fakeDC{
		ctx:   WithBudget(t.Context(), budget),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}

	// The request's own max_tokens is the output limit the provider applies,
	// so the reservation counts it too: 1000 input at 1000/million is 1
	// milli-point and a million output at 8000/million is 8000.
	cfg := Config{Name: "chat", Model: "sim-chat", Op: model.CapChat, InputTokens: 1000}
	if _, err := h.Handle(dc, cfg, json.RawMessage(`{"params":{"max_tokens":1000000}}`)); err != nil {
		t.Fatal(err)
	}
	if got := budget.Reserved(inv.refs[0]); got != 8001 {
		t.Fatalf("reserved = %d, want 8001 counting the request's max_tokens", got)
	}
}

func TestHandleFailsClosedWhenTheActualChargeCarriesTheRunPastItsBudget(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	// The estimate reserves one milli-point; the provider reports a million
	// output tokens, so the actual charge is 8001 and the run's ceiling is 10.
	inv.result = model.Result{
		State: model.StateSucceeded,
		Usage: &model.Usage{InputTokens: 1000, OutputTokens: 1_000_000},
	}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: tokenCatalog(t), Steps: reg})
	budget := NewMemory(10)
	dc := &fakeDC{
		ctx:   WithBudget(t.Context(), budget),
		now:   time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC),
		steps: reg.fns,
	}

	out, err := h.Handle(dc, Config{Name: "chat", Model: "sim-chat", Op: model.CapChat, InputTokens: 1000}, json.RawMessage(`{}`))
	if core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("err = %v, want budget_exceeded", err)
	}
	// The charge is recorded rather than hidden; the node fails so the run
	// stops spending after an understated reservation.
	if got := budget.Charged(inv.refs[0]); got != 8001 {
		t.Fatalf("charged = %d, want the actual 8001", got)
	}
	if out.CostMpts != 8001 {
		t.Fatalf("cost = %d, want the actual 8001", out.CostMpts)
	}
}

func TestHandleFailsClosedWhenAFailedInvocationsChargeCarriesTheRunPastItsBudget(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	// The estimate reserves 500; the failed invocation still reports 8001, so
	// the settlement carries the run's 600 ceiling past its limit.
	inv.result = model.Result{State: model.StateFailed, CostMpts: 8001}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	budget := NewMemory(600)

	out, err := runNode(t, h, reg, t.Context(), budget, `{"params":{"duration_s":5}}`)
	if core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("err = %v, want budget_exceeded, not the provider's failure", err)
	}
	// The charge is recorded rather than hidden, and the node fails so the
	// run stops spending after an understated reservation.
	ref := inv.refs[0]
	if got := budget.Charged(ref); got != 8001 {
		t.Fatalf("charged = %d, want the actual 8001", got)
	}
	if out.CostMpts != 8001 {
		t.Fatalf("cost = %d, want the actual 8001", out.CostMpts)
	}
	if !budget.Settled(ref) || budget.HeldTotal() != 0 {
		t.Fatalf("budget = held %d settled %v, want closed", budget.HeldTotal(), budget.Settled(ref))
	}
}

func TestHandleFailsClosedWhenACancelledInvocationsChargeCarriesTheRunPastItsBudget(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	// A cancelled invocation that reports a cost is settled by it; 8001 past
	// the run's 600 ceiling fails the node instead of only returning cancelled.
	inv.result = model.Result{State: model.StateCancelled, CostMpts: 8001}
	reg := newFakeRegistrar()
	h := testHandler(t, Deps{Invoker: inv, Catalog: videoCatalog(t), Steps: reg})
	budget := NewMemory(600)

	out, err := runNode(t, h, reg, t.Context(), budget, `{"params":{"duration_s":5}}`)
	if core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("err = %v, want budget_exceeded, not cancelled", err)
	}
	ref := inv.refs[0]
	if got := budget.Charged(ref); got != 8001 {
		t.Fatalf("charged = %d, want the actual 8001", got)
	}
	if out.CostMpts != 8001 {
		t.Fatalf("cost = %d, want the actual 8001", out.CostMpts)
	}
	if !budget.Settled(ref) || budget.HeldTotal() != 0 {
		t.Fatalf("budget = held %d settled %v, want closed", budget.HeldTotal(), budget.Settled(ref))
	}
}

func TestHandleFailsClosedWhenAnIngestFailuresSettlementCarriesTheRunPastItsBudget(t *testing.T) {
	t.Parallel()
	inv := newScriptInvoker()
	inv.start = func(_ int, req model.Request) (model.Handle, error) {
		return model.Handle{ClientRef: req.ClientRef}, nil
	}
	// The deferred settlement still runs after a failed ingest; its actual
	// charge (8001) is past the run's 600 ceiling, so the node fails closed
	// with budget_exceeded even though the ingest failed first.
	inv.result = model.Result{
		State:    model.StateSucceeded,
		CostMpts: 8001,
		Outputs:  []model.Output{{URL: "https://vendor.test/a.png", Media: "image"}},
	}
	reg := newFakeRegistrar()
	budget := NewMemory(600)
	h := testHandler(t, Deps{
		Invoker: inv,
		Catalog: videoCatalog(t),
		Steps:   reg,
		Blobs:   newMemBlob(),
		Fetch: func(context.Context, model.Output) ([]byte, string, error) {
			return nil, "", errors.New("the vendor is gone")
		},
	})

	_, err := runNode(t, h, reg, t.Context(), budget, `{"params":{"duration_s":5}}`)
	if core.CodeOf(err) != core.CodeBudgetExceeded {
		t.Fatalf("err = %v, want budget_exceeded", err)
	}
	ref := inv.refs[0]
	if got := budget.Charged(ref); got != 8001 {
		t.Fatalf("charged = %d, want the pinned actual 8001", got)
	}
	if !budget.Settled(ref) || budget.HeldTotal() != 0 {
		t.Fatalf("budget = held %d settled %v, want closed", budget.HeldTotal(), budget.Settled(ref))
	}
}
