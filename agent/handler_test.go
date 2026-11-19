package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"

	"github.com/epzero/basamento/agent"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/tool"
)

// --- a deterministic DurableContext ---------------------------------------

type fakeDC struct {
	mu      sync.Mutex
	now     time.Time
	ctx     context.Context
	journal map[string]json.RawMessage
	funcs   map[string]runtime.StepFunc
	calls   []string
	ids     int
}

func newDC(at time.Time) *fakeDC {
	return &fakeDC{
		now:     at.UTC(),
		ctx:     context.Background(),
		journal: map[string]json.RawMessage{},
		funcs:   map[string]runtime.StepFunc{},
	}
}

func (d *fakeDC) Context() context.Context { return d.ctx }
func (d *fakeDC) Now() time.Time           { d.mu.Lock(); defer d.mu.Unlock(); return d.now }
func (d *fakeDC) Advance(at time.Duration) { d.mu.Lock(); d.now = d.now.Add(at); d.mu.Unlock() }
func (d *fakeDC) NewID(kind string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ids++
	return fmt.Sprintf("%s_%d", kind, d.ids)
}
func (d *fakeDC) Rand(string) *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }
func (d *fakeDC) Child(runtime.FlowRef, any, runtime.ChildOptions) runtime.Future {
	return &future{}
}
func (d *fakeDC) Sleep(time.Duration) runtime.Future    { return &future{} }
func (d *fakeDC) Await(runtime.GateSpec) runtime.Future { return &future{} }
func (d *fakeDC) Select(...runtime.Future) (int, error) { return 0, nil }
func (d *fakeDC) Emit(runtime.ProgressEvent) error      { return nil }
func (d *fakeDC) Checkpoint(runtime.SnapshotRef) error  { return nil }
func (d *fakeDC) Capabilities() runtime.Caps            { return runtime.Caps{} }

func (d *fakeDC) Register(name string, fn runtime.StepFunc) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.funcs[name]; ok {
		return core.NewError(core.CodeConflict, "already registered", nil)
	}
	d.funcs[name] = fn
	return nil
}

func (d *fakeDC) Step(name string, in any, _ runtime.StepOptions) runtime.Future {
	d.mu.Lock()
	d.calls = append(d.calls, name)
	if raw, ok := d.journal[name]; ok {
		d.mu.Unlock()
		return &future{raw: raw}
	}
	fn := d.funcs[name]
	d.mu.Unlock()
	if fn == nil {
		return &future{err: core.NewError(core.CodeUnsupported, "step not registered", map[string]any{"step": name})}
	}
	rawIn, err := json.Marshal(in)
	if err != nil {
		return &future{err: err}
	}
	raw, err := fn(d.ctx, rawIn)
	if err != nil {
		return &future{err: err}
	}
	d.mu.Lock()
	d.journal[name] = append(json.RawMessage(nil), raw...)
	d.mu.Unlock()
	return &future{raw: raw}
}

func (d *fakeDC) stepCalls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

type future struct {
	raw json.RawMessage
	err error
}

func (f *future) Get(out any) error {
	if f.err != nil {
		return f.err
	}
	if out == nil || len(f.raw) == 0 {
		return nil
	}
	return json.Unmarshal(f.raw, out)
}
func (f *future) Ready() bool { return true }

// --- a scripted model ------------------------------------------------------

type scriptTurn struct {
	Text     string         // answer text, when no tool calls
	Tools    []string       // tool names requested
	Handoffs []string       // handoff targets requested
	Input    int64          // reported input tokens
	Output   int64          // reported output tokens
	Cost     int64          // actual charge
	Estimate agent.Estimate // reservation and expected input tokens
	Duration time.Duration  // run-clock time the turn takes
}

type scriptedModel struct {
	mu       sync.Mutex
	dc       *fakeDC
	turns    []scriptTurn
	invoked  int
	requests []model.Request
	log      *[]string
	node     string
}

func (m *scriptedModel) Describe(context.Context) ([]model.Descriptor, error) {
	return []model.Descriptor{{ID: "script"}}, nil
}
func (m *scriptedModel) Start(_ context.Context, r model.Request) (model.Handle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.log != nil {
		*m.log = append(*m.log, "invoke")
	}
	m.invoked++
	m.requests = append(m.requests, r)
	return model.Handle{ID: fmt.Sprint(m.invoked), ClientRef: r.ClientRef}, nil
}
func (m *scriptedModel) Events(context.Context, model.Handle, int64) (<-chan model.Event, error) {
	ch := make(chan model.Event)
	close(ch)
	return ch, nil
}
func (m *scriptedModel) Result(context.Context, model.Handle) (model.Result, error) {
	m.mu.Lock()
	if len(m.turns) == 0 {
		m.mu.Unlock()
		return model.Result{}, core.NewError(core.CodeInternal, "script exhausted", nil)
	}
	turn := m.turns[0]
	m.turns = m.turns[1:]
	m.mu.Unlock()
	if turn.Duration > 0 {
		m.dc.Advance(turn.Duration)
	}
	msg := &schema.Message{Role: schema.Assistant, Content: turn.Text, Name: fmt.Sprintf("%s/turn-%d", m.node, m.invoked)}
	for i, name := range turn.Tools {
		msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
			ID:       fmt.Sprintf("%s/turn-%d/call-%d", m.node, m.invoked, i+1),
			Type:     "function",
			Function: schema.FunctionCall{Name: name, Arguments: `{"q":"x"}`},
		})
	}
	for i, target := range turn.Handoffs {
		msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
			ID:       fmt.Sprintf("%s/turn-%d/call-%d", m.node, m.invoked, len(turn.Tools)+i+1),
			Type:     "function",
			Function: schema.FunctionCall{Name: "handoff_to", Arguments: fmt.Sprintf(`{"target":%q}`, target)},
		})
	}
	raw, _ := json.Marshal(msg)
	usage := model.Usage{InputTokens: turn.Input, OutputTokens: turn.Output}
	return model.Result{State: model.StateSucceeded, Message: raw, Usage: &usage, CostMpts: turn.Cost}, nil
}
func (m *scriptedModel) Cancel(context.Context, model.Handle) error { return nil }
func (m *scriptedModel) Lookup(context.Context, string) (*model.Handle, error) {
	return nil, nil
}

func (m *scriptedModel) Estimate(context.Context, model.Request) (agent.Estimate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.turns) == 0 {
		return agent.Estimate{}, nil
	}
	return m.turns[0].Estimate, nil
}

// --- a tool runner ---------------------------------------------------------

type toolRunner struct {
	mu      sync.Mutex
	dc      *fakeDC
	results map[string]tool.Outcome
	durs    map[string]time.Duration
	calls   map[string]int
}

func newTools(dc *fakeDC) *toolRunner {
	return &toolRunner{dc: dc, results: map[string]tool.Outcome{}, durs: map[string]time.Duration{}, calls: map[string]int{}}
}

func (r *toolRunner) run(_ context.Context, _ *tool.Set, call tool.Call) (tool.Outcome, error) {
	r.mu.Lock()
	r.calls[call.Name]++
	out := r.results[call.Name]
	d := r.durs[call.Name]
	r.mu.Unlock()
	if d > 0 {
		r.dc.Advance(d)
	}
	return out, nil
}

func (r *toolRunner) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[name]
}

// --- helpers ---------------------------------------------------------------

type recorder struct {
	mu     sync.Mutex
	events []string
	res    map[int]int64
	settle map[int]int64
}

func (r *recorder) Reserved(turn int, mpts int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf("reserve-%d=%d", turn, mpts))
	r.res[turn] = mpts
}
func (r *recorder) Settled(turn int, charged int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, fmt.Sprintf("settle-%d=%d", turn, charged))
	r.settle[turn] = charged
}
func (r *recorder) Dropped(int, int) {}

func newHandler(dc *fakeDC, m *scriptedModel, tr *toolRunner, obs agent.Observer) *agent.Handler {
	return agent.New(agent.Options{
		Models: func(string) (model.Invoker, error) { return m, nil },
		Estimate: func(ctx context.Context, req model.Request) (agent.Estimate, error) {
			return m.Estimate(ctx, req)
		},
		RunTool:  tr.run,
		Tools:    func(string) *tool.Set { return &tool.Set{Tools: []tool.Tool{{Name: "search"}, {Name: "fetch"}}} },
		Defines:  func(node string) bool { return node != "ghost" && node != "reviewer" },
		Register: dc.Register,
		Observer: obs,
	})
}

func spec(t *testing.T, v map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func runNode(t *testing.T, h *agent.Handler, dc *fakeDC, name string, nodeSpec map[string]any, input json.RawMessage) (agent.Output, error) {
	t.Helper()
	raw, err := h.Handle(dc, agent.Node{Name: name, Spec: spec(t, nodeSpec)}, input)
	if raw == nil {
		return agent.Output{}, err
	}
	var out agent.Output
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("output is not JSON: %v (%s)", err, raw)
	}
	return out, err
}

// --- tests -----------------------------------------------------------------

func TestEachModelTurnIsOneStep(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{{Text: "Done.", Estimate: agent.Estimate{}}}}
	h := newHandler(dc, m, newTools(dc), nil)
	out, err := runNode(t, h, dc, "worker", map[string]any{"model": "script"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != agent.StateCompleted || out.Answer != "Done." {
		t.Fatalf("output = %+v", out)
	}
	if len(out.Turns) != 1 {
		t.Fatalf("turns = %d, want 1", len(out.Turns))
	}
	if got := dc.stepCalls(); len(got) != 1 || got[0] != "worker/turn-1" {
		t.Fatalf("steps = %v", got)
	}
}

func TestToolResultIsFedToTheNextTurn(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{
		{Tools: []string{"search"}, Input: 10, Output: 5},
		{Text: "Done."},
	}}
	tr := newTools(dc)
	tr.results["search"] = tool.Outcome{Result: tool.Result{Content: json.RawMessage(`"42"`)}}
	h := newHandler(dc, m, tr, nil)
	out, err := runNode(t, h, dc, "worker", map[string]any{"model": "script"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Answer != "Done." || len(out.Turns) != 2 {
		t.Fatalf("output = %+v", out)
	}
	if tr.count("search") != 1 {
		t.Fatalf("search ran %d times", tr.count("search"))
	}
	if !messagesContain(t, m.requests[1].Messages, `"content":"42"`) {
		t.Fatalf("turn 2 messages do not carry the tool result: %s", m.requests[1].Messages)
	}
}

func TestFailedToolCallFailsTheRun(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{{Tools: []string{"search"}}}}
	tr := newTools(dc)
	tr.results["search"] = tool.Outcome{Failed: true, Code: core.CodeTimeout, Message: "slow"}
	h := newHandler(dc, m, tr, nil)
	_, err := runNode(t, h, dc, "worker", map[string]any{"model": "script"}, nil)
	if core.CodeOf(err) != core.CodeTimeout {
		t.Fatalf("err = %v, want timeout", err)
	}
	if m.invoked != 1 {
		t.Fatalf("invocations = %d, want 1", m.invoked)
	}
}

func TestStepCapStopsBeforeTheExtraTurn(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{{Tools: []string{"search"}}, {Tools: []string{"search"}}, {Tools: []string{"search"}}}}
	h := newHandler(dc, m, newTools(dc), nil)
	out, err := runNode(t, h, dc, "worker", map[string]any{"model": "script", "caps": map[string]any{"steps": 2}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != agent.StateStepCap || len(out.Turns) != 2 {
		t.Fatalf("output = %+v", out)
	}
	if m.invoked != 2 {
		t.Fatalf("invocations = %d, want 2", m.invoked)
	}
}

func TestCostReservationIsBeforeTheTurn(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	log := []string{}
	obs := &recorder{res: map[int]int64{}, settle: map[int]int64{}}
	m := &scriptedModel{dc: dc, node: "worker", log: &log, turns: []scriptTurn{{
		Text: "Done.", Estimate: agent.Estimate{CostMpts: 500}, Cost: 400,
	}}}
	h := newHandler(dc, m, newTools(dc), obs)
	_, err := runNode(t, h, dc, "worker", map[string]any{"model": "script", "caps": map[string]any{"cost_mpts": 1000}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if obs.res[1] != 500 || obs.settle[1] != 400 {
		t.Fatalf("reserved %d settled %d, want 500/400", obs.res[1], obs.settle[1])
	}
	if got := strings.Join(log, ","); got != "invoke" {
		t.Fatalf("log = %q", got)
	}
	obs.mu.Lock()
	seq := strings.Join(obs.events, ",")
	obs.mu.Unlock()
	if seq != "reserve-1=500,settle-1=400" {
		t.Fatalf("events = %q", seq)
	}
}

func TestCostCapStopsBeforeATurnOverIt(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{
		{Tools: []string{"search"}, Estimate: agent.Estimate{CostMpts: 600}, Cost: 600},
		{Tools: []string{"search"}, Estimate: agent.Estimate{CostMpts: 600}, Cost: 600},
	}}
	h := newHandler(dc, m, newTools(dc), nil)
	out, err := runNode(t, h, dc, "worker", map[string]any{"model": "script", "caps": map[string]any{"cost_mpts": 1000}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != agent.StateCostCap || len(out.Turns) != 1 || m.invoked != 1 {
		t.Fatalf("output = %+v invocations = %d", out, m.invoked)
	}
}

func TestSpendingExactlyTheCostCapDoesNotStop(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{
		{Tools: []string{"search"}, Estimate: agent.Estimate{CostMpts: 1000}, Cost: 1000},
		{Text: "Done.", Estimate: agent.Estimate{CostMpts: 0}, Cost: 0},
	}}
	h := newHandler(dc, m, newTools(dc), nil)
	out, err := runNode(t, h, dc, "worker", map[string]any{"model": "script", "caps": map[string]any{"cost_mpts": 1000}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Answer != "Done." || m.invoked != 2 {
		t.Fatalf("output = %+v invocations = %d", out, m.invoked)
	}
}

func TestTokenCapStopsBeforeATurnOverIt(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{
		{Tools: []string{"search"}, Input: 5000, Output: 1000, Estimate: agent.Estimate{InputTokens: 5000}},
		{Tools: []string{"search"}, Input: 5000, Output: 1000, Estimate: agent.Estimate{InputTokens: 5000}},
	}}
	h := newHandler(dc, m, newTools(dc), nil)
	out, err := runNode(t, h, dc, "worker", map[string]any{"model": "script", "caps": map[string]any{"tokens": 10000}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != agent.StateTokenCap || len(out.Turns) != 1 || m.invoked != 1 {
		t.Fatalf("output = %+v invocations = %d", out, m.invoked)
	}
}

func TestTimeCapStopsBeforeTheNextTurn(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{
		{Tools: []string{"search"}},
		{Tools: []string{"search"}},
	}}
	tr := newTools(dc)
	tr.durs["search"] = 60 * time.Second
	h := newHandler(dc, m, tr, nil)
	out, err := runNode(t, h, dc, "worker", map[string]any{"model": "script", "caps": map[string]any{"time": "1m"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != agent.StateTimeCap || len(out.Turns) != 1 || m.invoked != 1 {
		t.Fatalf("output = %+v invocations = %d", out, m.invoked)
	}
}

func TestCancellationBetweenTurnsStartsNoTurn(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	ctx, cancel := context.WithCancel(context.Background())
	dc.ctx = ctx
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{{Tools: []string{"fetch"}}, {Text: "Done."}}}
	tr := &toolRunner{dc: dc, results: map[string]tool.Outcome{}, durs: map[string]time.Duration{}, calls: map[string]int{}}
	// Cancel once the first tool call is done: the next boundary must stop.
	wrapped := &cancelAfterTool{inner: tr, cancel: cancel}
	h := agent.New(agent.Options{
		Models:   func(string) (model.Invoker, error) { return m, nil },
		Estimate: m.Estimate,
		RunTool:  wrapped.run,
		Tools:    func(string) *tool.Set { return nil },
		Defines:  func(string) bool { return true },
		Register: dc.Register,
	})
	_, err := runNode(t, h, dc, "worker", map[string]any{"model": "script"}, nil)
	if core.CodeOf(err) != core.CodeCancelled {
		t.Fatalf("err = %v, want cancelled", err)
	}
	if m.invoked != 1 {
		t.Fatalf("invocations = %d, want 1", m.invoked)
	}
}

type cancelAfterTool struct {
	inner  *toolRunner
	cancel context.CancelFunc
}

func (c *cancelAfterTool) run(ctx context.Context, set *tool.Set, call tool.Call) (tool.Outcome, error) {
	out, err := c.inner.run(ctx, set, call)
	c.cancel()
	return out, err
}

func TestHandoffCarriesTheTurnsAndStartsTheTarget(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	triage := &scriptedModel{dc: dc, node: "triage", turns: []scriptTurn{
		{Tools: []string{"search"}},
		{Handoffs: []string{"fixer"}},
	}}
	tr := newTools(dc)
	tr.results["search"] = tool.Outcome{Result: tool.Result{Content: json.RawMessage(`"42"`)}}
	h := newHandler(dc, triage, tr, nil)
	out, err := runNode(t, h, dc, "triage", map[string]any{"model": "script"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != agent.StateHandoff || out.Target != "fixer" || len(out.Turns) != 2 {
		t.Fatalf("output = %+v", out)
	}
	if out.StartedAt == nil || !out.StartedAt.Equal(dc.Now()) {
		t.Fatalf("started_at = %v", out.StartedAt)
	}
	raw, _ := json.Marshal(out)
	fixer := &scriptedModel{dc: dc, node: "fixer", turns: []scriptTurn{{Text: "Fixed."}}}
	fh := newHandler(dc, fixer, tr, nil)
	_, err = runNode(t, fh, dc, "fixer", map[string]any{"model": "script"}, raw)
	if err != nil {
		t.Fatal(err)
	}
	msgs := string(fixer.requests[0].Messages)
	for _, want := range []string{`"name":"triage/turn-1"`, `"name":"triage/turn-2"`, `"name":"handoff_to"`, `"content":"42"`} {
		if !strings.Contains(msgs, want) {
			t.Fatalf("fixer turn 1 messages lack %s: %s", want, msgs)
		}
	}
}

func TestHandoffToAnUndefinedAgentFails(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "triage", turns: []scriptTurn{{Handoffs: []string{"ghost"}}}}
	h := newHandler(dc, m, newTools(dc), nil)
	_, err := runNode(t, h, dc, "triage", map[string]any{"model": "script"}, nil)
	if core.CodeOf(err) != core.CodeNotFound || !strings.Contains(err.Error(), "ghost") {
		t.Fatalf("err = %v, want not_found naming ghost", err)
	}
}

func TestMultipleHandoffsFailWithoutAToolStep(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "triage", turns: []scriptTurn{{Handoffs: []string{"fixer", "reviewer"}}}}
	h := newHandler(dc, m, newTools(dc), nil)
	_, err := runNode(t, h, dc, "triage", map[string]any{"model": "script"}, nil)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	for _, name := range dc.stepCalls() {
		if strings.Contains(name, "/tool-") {
			t.Fatalf("made tool step %s", name)
		}
	}
}

func TestMixedToolAndHandoffHandsOffWithoutTheTool(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "triage", turns: []scriptTurn{{Tools: []string{"search"}, Handoffs: []string{"fixer"}}}}
	tr := newTools(dc)
	h := newHandler(dc, m, tr, nil)
	out, err := runNode(t, h, dc, "triage", map[string]any{"model": "script"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != agent.StateHandoff || tr.count("search") != 0 {
		t.Fatalf("output = %+v search calls = %d", out, tr.count("search"))
	}
}

func TestTargetAppliesItsOwnStepCap(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	triage := &scriptedModel{dc: dc, node: "triage", turns: []scriptTurn{{Handoffs: []string{"fixer"}}}}
	h := newHandler(dc, triage, newTools(dc), nil)
	out, err := runNode(t, h, dc, "triage", map[string]any{"model": "script", "caps": map[string]any{"steps": 1}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	fixer := &scriptedModel{dc: dc, node: "fixer", turns: []scriptTurn{{Tools: []string{"search"}}, {Tools: []string{"search"}}, {Tools: []string{"search"}}}}
	fh := newHandler(dc, fixer, newTools(dc), nil)
	fout, err := runNode(t, fh, dc, "fixer", map[string]any{"model": "script", "caps": map[string]any{"steps": 2}}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if fout.State != agent.StateStepCap || fixer.invoked != 2 {
		t.Fatalf("output = %+v invocations = %d", fout, fixer.invoked)
	}
}

func TestTimeCapSpansTheHandoff(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	triage := &scriptedModel{dc: dc, node: "triage", turns: []scriptTurn{{Handoffs: []string{"fixer"}, Duration: 40 * time.Second}}}
	h := newHandler(dc, triage, newTools(dc), nil)
	out, err := runNode(t, h, dc, "triage", map[string]any{"model": "script", "caps": map[string]any{"time": "1m"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.TimeCap == nil || formatCap(out) != "1m" {
		t.Fatalf("time cap = %s", formatCap(out))
	}
	raw, _ := json.Marshal(out)
	fixer := &scriptedModel{dc: dc, node: "fixer", turns: []scriptTurn{
		{Tools: []string{"search"}, Duration: 20 * time.Second},
		{Tools: []string{"search"}},
	}}
	fh := newHandler(dc, fixer, newTools(dc), nil)
	fout, err := runNode(t, fh, dc, "fixer", map[string]any{"model": "script"}, raw)
	if err != nil {
		t.Fatal(err)
	}
	if fout.State != agent.StateTimeCap || fixer.invoked != 1 {
		t.Fatalf("output = %+v invocations = %d", fout, fixer.invoked)
	}
}

func formatCap(out agent.Output) string {
	if out.TimeCap == nil {
		return "null"
	}
	raw, _ := json.Marshal(out.TimeCap)
	return strings.Trim(string(raw), `"`)
}

func TestReplayFeedsRecordedTurnsAndDoesNotPayAgain(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	// Record turn 1 and its tool call as a previous execution would have.
	turn := map[string]any{
		"type": "message.completed",
		"message": map[string]any{"role": "assistant", "content": "Searching.", "name": "worker/turn-1", "tool_calls": []map[string]any{{
			"id": "worker/turn-1/call-1", "type": "function", "function": map[string]any{"name": "search", "arguments": `{"q":"x"}`},
		}}},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	}
	dc.journal["worker/turn-1"] = spec(t, turn)
	dc.journal["worker/tool-1-1"] = spec(t, map[string]any{"result": map[string]any{"content": "42"}})

	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{{Text: "Done."}}}
	tr := newTools(dc)
	h := newHandler(dc, m, tr, nil)
	out, err := runNode(t, h, dc, "worker", map[string]any{"model": "script"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Answer != "Done." || m.invoked != 1 || tr.count("search") != 0 {
		t.Fatalf("output = %+v invocations = %d tool calls = %d", out, m.invoked, tr.count("search"))
	}
	msgs := string(m.requests[0].Messages)
	if !strings.Contains(msgs, `"content":"42"`) || !strings.Contains(msgs, `"name":"worker/turn-1"`) {
		t.Fatalf("resumed turn 2 messages lack the record: %s", msgs)
	}
}

func TestMemoryPolicyKeepsStartAndLatestTurns(t *testing.T) {
	turns := []agent.Turn{
		{Message: msg("turn-1"), Tokens: 300, Results: []json.RawMessage{result("r1")}},
		{Message: msg("turn-2"), Tokens: 400, Results: []json.RawMessage{result("r2")}},
		{Message: msg("turn-3"), Tokens: 400, Results: []json.RawMessage{result("r3")}},
	}
	system := []agent.SystemMessage{{Content: "sys", Tokens: 100}}
	msgs, dropped, err := agent.BuildMessagesForTest(system, turns, 1000)
	if err != nil {
		t.Fatal(err)
	}
	names := messageNames(msgs)
	if !contains(names, "turn-1") || !contains(names, "turn-3") || contains(names, "turn-2") {
		t.Fatalf("kept %v", names)
	}
	if dropped != 2 {
		t.Fatalf("dropped %d messages, want 2", dropped)
	}
}

func TestMemoryPolicyFailsWhenTheKeptSetIsOverTheLimit(t *testing.T) {
	system := []agent.SystemMessage{{Content: "sys", Tokens: 1200}}
	_, _, err := agent.BuildMessagesForTest(system, nil, 1000)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
}

func TestMemoryPolicyWithinTheLimitKeepsWholeTurns(t *testing.T) {
	turns := []agent.Turn{{Message: msg("turn-1"), Tokens: 400}, {Message: msg("turn-2"), Tokens: 500}}
	msgs, dropped, err := agent.BuildMessagesForTest(nil, turns, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if dropped != 0 || len(msgs) != 2 {
		t.Fatalf("dropped %d messages, kept %d", dropped, len(msgs))
	}
}

// TestMemoryPolicyProperty checks the retention invariant over random token
// costs: whatever is kept stays within the limit, includes the first turn, and
// is the first turn plus a suffix of the latest turns.
func TestMemoryPolicyProperty(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 11))
	for i := 0; i < 500; i++ {
		n := 1 + r.IntN(8)
		turns := make([]agent.Turn, n)
		var total int64
		for j := range turns {
			tokens := int64(1 + r.IntN(300))
			turns[j] = agent.Turn{Message: msg(fmt.Sprintf("turn-%d", j+1)), Tokens: tokens}
			total += tokens
		}
		sys := int64(r.IntN(100))
		limit := sys + 1 + r.Int64N(total+1)
		system := []agent.SystemMessage{{Content: "sys", Tokens: sys}}
		msgs, _, err := agent.BuildMessagesForTest(system, turns, limit)
		if err != nil {
			continue // the kept set legitimately does not fit
		}
		names := messageNames(msgs)
		if !contains(names, "turn-1") {
			t.Fatalf("turn 1 dropped: %v", names)
		}
		// Kept turns after the first are a suffix of the latest turns: if turn
		// k (k >= 2) is kept, every later turn is kept too.
		var keptAfter []int
		for j := 1; j < len(turns); j++ {
			if contains(names, fmt.Sprintf("turn-%d", j+1)) {
				keptAfter = append(keptAfter, j+1)
			}
		}
		for i, k := range keptAfter {
			if k != len(turns)-len(keptAfter)+i+1 {
				t.Fatalf("kept turns %v are not a suffix", keptAfter)
			}
		}
	}
}

// --- helpers ---------------------------------------------------------------

func msg(name string) json.RawMessage {
	raw, _ := json.Marshal(&schema.Message{Role: schema.Assistant, Content: name, Name: name})
	return raw
}
func result(text string) json.RawMessage {
	raw, _ := json.Marshal(&schema.Message{Role: schema.Tool, Content: text})
	return raw
}
func messageNames(msgs []*schema.Message) []string {
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Name)
	}
	return out
}
func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
func messagesContain(t *testing.T, raw json.RawMessage, want string) bool {
	t.Helper()
	return strings.Contains(string(raw), want)
}
