package node

import (
	"context"
	"encoding/json"
	"errors"
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
	fn, ok := d.steps[name]
	if !ok {
		return doneFuture{err: core.NewError(core.CodeUnsupported, "step not registered", map[string]any{"step": name})}
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return doneFuture{err: err}
	}
	out, err := fn(d.ctx, raw)
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
	if len(dc.awaited) != 1 || dc.awaited[0] != AmbiguousStartGate {
		t.Fatalf("awaited = %v, want [%s]", dc.awaited, AmbiguousStartGate)
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
