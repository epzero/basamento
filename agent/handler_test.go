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
	Text      string         // answer text, when no tool calls
	Tools     []string       // tool names requested
	Handoffs  []string       // handoff targets requested
	Input     int64          // reported input tokens
	Output    int64          // reported output tokens
	Cost      int64          // actual charge
	Estimate  agent.Estimate // reservation and expected input tokens
	Duration  time.Duration  // run-clock time the turn takes
	State     model.State    // result state; empty means succeeded
	Err       error          // result error, with Cost charged
	NoMessage bool           // result carries no message
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
	state := turn.State
	if state == "" {
		state = model.StateSucceeded
	}
	res := model.Result{State: state, Usage: &usage, CostMpts: turn.Cost}
	if !turn.NoMessage {
		res.Message = raw
	}
	return res, turn.Err
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
		RunTool: tr.run,
		Tools: func(context.Context, string) (*tool.Set, error) {
			return &tool.Set{Tools: []tool.Tool{{Name: "search"}, {Name: "fetch"}}}, nil
		},
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
	out, _, err := runNodeResult(t, h, dc, name, nodeSpec, input)
	return out, err
}

func runNodeResult(t *testing.T, h *agent.Handler, dc *fakeDC, name string, nodeSpec map[string]any, input json.RawMessage) (agent.Output, agent.Result, error) {
	t.Helper()
	res, err := h.Handle(dc, agent.Node{Name: name, Spec: spec(t, nodeSpec)}, input)
	if len(res.Output) == 0 {
		return agent.Output{}, res, err
	}
	var out agent.Output
	if err := json.Unmarshal(res.Output, &out); err != nil {
		t.Fatalf("output is not JSON: %v (%s)", err, res.Output)
	}
	return out, res, err
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
	if got := dc.stepCalls(); len(got) != 1 || got[0] != "worker/agent_1/turn-1" {
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

// AL-01: the time cap is checked before every model turn. With checks > 1 a
// non-boundary turn must still stop when the cap is reached.
func TestTimeCapIsCheckedEveryTurn(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{
		{Tools: []string{"search"}},
		{Tools: []string{"search"}},
	}}
	tr := newTools(dc)
	tr.durs["search"] = 60 * time.Second
	h := newHandler(dc, m, tr, nil)
	out, err := runNode(t, h, dc, "worker", map[string]any{
		"model": "script", "checks": 2, "caps": map[string]any{"time": "1m"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != agent.StateTimeCap || len(out.Turns) != 1 || m.invoked != 1 {
		t.Fatalf("output = %+v invocations = %d", out, m.invoked)
	}
}

// R1-1: a model turn that was charged but failed still settles its cost, so
// the host journals node_failed with what the ledger charged.
func TestFailedTurnSettlesItsCost(t *testing.T) {
	cases := []struct {
		name string
		turn scriptTurn
	}{
		{"invoke error", scriptTurn{Cost: 40, Err: core.NewError(core.CodeUnavailable, "model down", nil)}},
		{"state not succeeded", scriptTurn{Cost: 25, State: model.StateFailed}},
		{"empty message", scriptTurn{Cost: 15, NoMessage: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
			m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{tc.turn}}
			h := newHandler(dc, m, newTools(dc), nil)
			_, res, err := runNodeResult(t, h, dc, "worker", map[string]any{"model": "script"}, nil)
			if err == nil {
				t.Fatal("run succeeded, want an error")
			}
			if res.CostMpts != tc.turn.Cost {
				t.Fatalf("settled %d milli-points, want %d", res.CostMpts, tc.turn.Cost)
			}
		})
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
		Tools:    func(context.Context, string) (*tool.Set, error) { return nil, nil },
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
	dc.journal["worker/agent_1/turn-1"] = spec(t, turn)
	dc.journal["worker/agent_1/tool-1-1"] = spec(t, map[string]any{"result": map[string]any{"content": "42"}})

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

// AL-02: a system message that declares no tokens is weighed by its content,
// not treated as free, when the run has an input token limit.
func TestMemoryPolicyWeighsSystemMessagesWithoutTokens(t *testing.T) {
	system := []agent.SystemMessage{{Content: strings.Repeat("x", 4000)}}
	_, _, err := agent.BuildMessagesForTest(system, nil, 1000)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument; the system message was free", err)
	}
}

// AL-02: a model turn with no recorded usage is weighed by its content, not
// treated as free; with no input token limit it is still kept.
func TestMemoryPolicyWeighsTurnsWithoutUsage(t *testing.T) {
	raw, _ := json.Marshal(&schema.Message{Role: schema.Assistant, Content: strings.Repeat("x", 4000)})
	turns := []agent.Turn{{Message: raw}}
	_, _, err := agent.BuildMessagesForTest(nil, turns, 1000)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument; the turn was free", err)
	}
	if _, _, err := agent.BuildMessagesForTest(nil, turns, 0); err != nil {
		t.Fatalf("no limit: %v", err)
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

// --- reviewer findings (EP0-33, L1-01, L1-02) ------------------------------

// L1-01: a tool result reports no usage of its own, but it is still input to
// the next turn. A large one must weigh in the policy: here the recorded turn
// usage alone (300+300) fits 1000, but the first turn's 4000-byte result does
// not, so the run fails with invalid_argument instead of invoking the model.
func TestMemoryPolicyCountsToolResults(t *testing.T) {
	big := strings.Repeat("x", 4000)
	turns := []agent.Turn{
		{Message: msg("turn-1"), Tokens: 300, Results: []json.RawMessage{result(big)}},
		{Message: msg("turn-2"), Tokens: 300},
	}
	_, _, err := agent.BuildMessagesForTest(nil, turns, 1000)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument; the tool result was not counted", err)
	}
}

// L1-01: result weight also decides what is dropped. Turn 2's 4000-byte
// result does not fit the remainder, so that whole turn goes — and its result
// with it — while turns 1 and 3 stay, as in AGENT-024-01.
func TestMemoryPolicyDropsATurnWhoseResultsDoNotFit(t *testing.T) {
	big := strings.Repeat("x", 4000)
	turns := []agent.Turn{
		{Message: msg("turn-1"), Tokens: 300, Results: []json.RawMessage{result("ok")}},
		{Message: msg("turn-2"), Tokens: 300, Results: []json.RawMessage{result(big)}},
		{Message: msg("turn-3"), Tokens: 300, Results: []json.RawMessage{result("ok")}},
	}
	msgs, dropped, err := agent.BuildMessagesForTest(nil, turns, 1000)
	if err != nil {
		t.Fatal(err)
	}
	names := messageNames(msgs)
	if contains(names, "turn-2") {
		t.Fatalf("kept %v; turn 2 and its result exceed the remainder", names)
	}
	if !contains(names, "turn-1") || !contains(names, "turn-3") {
		t.Fatalf("kept %v, want turns 1 and 3", names)
	}
	if dropped != 2 {
		t.Fatalf("dropped %d messages, want 2 (turn 2 and its result)", dropped)
	}
}

// L1-01: the post-build guarantee measures the assembled request with the
// estimator, so a request over input_tokens fails before the invoke even when
// the recorded usage looked fine.
func TestAssembledRequestOverTheInputLimitFails(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{
		{Text: "Done.", Estimate: agent.Estimate{InputTokens: 1500}},
	}}
	h := newHandler(dc, m, newTools(dc), nil)
	_, err := runNode(t, h, dc, "worker", map[string]any{"model": "script", "input_tokens": 1000}, nil)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("err = %v, want invalid_argument", err)
	}
	if m.invoked != 0 {
		t.Fatalf("model invoked %d times, want 0", m.invoked)
	}
}

// L1-02: a node with handoff targets offers the reserved handoff_to tool, with
// the target as a required enum, so a real model can request a handoff.
func TestHandoffToolIsOfferedToTheModel(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "triage", turns: []scriptTurn{{Text: "Done."}}}
	h := agent.New(agent.Options{
		Models:   func(string) (model.Invoker, error) { return m, nil },
		Estimate: m.Estimate,
		RunTool:  newTools(dc).run,
		Tools: func(context.Context, string) (*tool.Set, error) {
			return &tool.Set{Tools: []tool.Tool{{Name: "search"}}}, nil
		},
		Defines:  func(node string) bool { return node == "triage" || node == "fixer" },
		Register: dc.Register,
	})
	node := agent.Node{Name: "triage", Spec: spec(t, map[string]any{"model": "script"}), Handoffs: []string{"fixer"}}
	if _, err := h.Handle(dc, node, nil); err != nil {
		t.Fatal(err)
	}
	if len(m.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(m.requests))
	}
	var infos []*schema.ToolInfo
	if err := json.Unmarshal(m.requests[0].Tools, &infos); err != nil {
		t.Fatalf("tools are not ToolInfo: %v (%s)", err, m.requests[0].Tools)
	}
	var handoff *schema.ToolInfo
	for _, info := range infos {
		if info.Name == "handoff_to" {
			handoff = info
		}
	}
	if handoff == nil || handoff.ParamsOneOf == nil {
		t.Fatalf("tools = %s, want a handoff_to tool with a schema", m.requests[0].Tools)
	}
	js, err := handoff.ToJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	if len(js.Required) != 1 || js.Required[0] != "target" {
		t.Fatalf("required = %v, want [target]", js.Required)
	}
	prop, ok := js.Properties.Get("target")
	if !ok || len(prop.Enum) != 1 || prop.Enum[0] != "fixer" {
		t.Fatalf("target schema = %+v, want enum [fixer]", prop)
	}
}

// L1-02: a node without handoff targets offers no handoff_to tool.
func TestNoHandoffToolWithoutTargets(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{{Text: "Done."}}}
	h := newHandler(dc, m, newTools(dc), nil)
	if _, err := runNode(t, h, dc, "worker", map[string]any{"model": "script"}, nil); err != nil {
		t.Fatal(err)
	}
	if messagesContain(t, m.requests[0].Tools, "handoff_to") {
		t.Fatalf("tools = %s, want no handoff_to", m.requests[0].Tools)
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

// --- self-review fixes (EP0-33, A33-1 … A33-5) -----------------------------

// A33-1: a flow can visit the same agent node twice in one run (a cycle, or a
// handoff back). Each visit must journal its own turn steps and invoke the
// model, instead of replaying the first visit's recorded turn.
func TestEachVisitToAnAgentNodeInvokesTheModel(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{{Text: "first"}, {Text: "second"}}}
	h := newHandler(dc, m, newTools(dc), nil)

	out, err := runNode(t, h, dc, "worker", map[string]any{"model": "script"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Answer != "first" {
		t.Fatalf("first visit answer = %q", out.Answer)
	}
	out, err = runNode(t, h, dc, "worker", map[string]any{"model": "script"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Answer != "second" || m.invoked != 2 {
		t.Fatalf("second visit answer = %q, invocations = %d; want the model invoked twice", out.Answer, m.invoked)
	}
	got := dc.stepCalls()
	if len(got) != 2 || got[0] == got[1] {
		t.Fatalf("step names = %v, want one distinct turn step per visit", got)
	}
}

// A33-2: the handler returns the settled cost, and that cost is what makes a
// cost guard trip: the flow host adds Result.CostMpts to the run's spend and
// the machine refuses the next loop visit once the guard's budget is passed.
func TestSettledCostReachesACostGuard(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{
		{Text: "one", Cost: 40},
		{Text: "two", Cost: 40},
		{Text: "three", Cost: 40},
	}}
	h := newHandler(dc, m, newTools(dc), nil)

	const guard = int64(50)
	var spent int64
	visits := 0
	for range 10 {
		if spent > guard {
			break
		}
		_, res, err := runNodeResult(t, h, dc, "worker", map[string]any{"model": "script"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.CostMpts == 0 {
			t.Fatal("Handle reported no settled cost, so a cost guard can never trip")
		}
		spent += res.CostMpts
		visits++
	}
	if visits != 2 || spent != 80 || m.invoked != 2 {
		t.Fatalf("visits = %d spent = %d invocations = %d; want 2, 80, 2", visits, spent, m.invoked)
	}
}

// A33-3: a tool's Input JSON Schema reaches the model as ParamsOneOf. Without
// it eino tells the model the tool takes no arguments.
func TestToolInputSchemaReachesTheModel(t *testing.T) {
	dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
	m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{{Text: "Done."}}}
	h := agent.New(agent.Options{
		Models:   func(string) (model.Invoker, error) { return m, nil },
		Estimate: m.Estimate,
		RunTool:  newTools(dc).run,
		Tools: func(context.Context, string) (*tool.Set, error) {
			return &tool.Set{Tools: []tool.Tool{{
				Name:        "search",
				Description: "find things",
				Input:       json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`),
			}}}, nil
		},
		Register: dc.Register,
	})
	if _, err := runNode(t, h, dc, "worker", map[string]any{"model": "script"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(m.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(m.requests))
	}
	var infos []*schema.ToolInfo
	if err := json.Unmarshal(m.requests[0].Tools, &infos); err != nil {
		t.Fatalf("tools are not ToolInfo: %v (%s)", err, m.requests[0].Tools)
	}
	if len(infos) != 1 || infos[0].ParamsOneOf == nil {
		t.Fatalf("tool info = %+v, want a parameter schema", infos)
	}
	js, err := infos[0].ToJSONSchema()
	if err != nil {
		t.Fatal(err)
	}
	if len(js.Required) != 1 || js.Required[0] != "q" {
		t.Fatalf("required = %v, want [q]", js.Required)
	}
}

// A33-5: the tool resolver receives the run's context, so two tenants sharing
// one handler are offered their own tool sets.
func TestToolSetResolutionSeesTheTenant(t *testing.T) {
	toolsFor := func(ctx context.Context, _ string) (*tool.Set, error) {
		id, ok := core.TenantFrom(ctx)
		if !ok {
			return nil, nil
		}
		name := "search"
		if id == "tenant-b" {
			name = "fetch"
		}
		return &tool.Set{Tools: []tool.Tool{{Name: name}}}, nil
	}
	run := func(tenant core.TenantID) []string {
		dc := newDC(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC))
		dc.ctx = core.WithTenant(context.Background(), tenant)
		m := &scriptedModel{dc: dc, node: "worker", turns: []scriptTurn{{Text: "Done."}}}
		h := agent.New(agent.Options{
			Models:   func(string) (model.Invoker, error) { return m, nil },
			Estimate: m.Estimate,
			RunTool:  newTools(dc).run,
			Tools:    toolsFor,
			Register: dc.Register,
		})
		if _, err := runNode(t, h, dc, "worker", map[string]any{"model": "script"}, nil); err != nil {
			t.Fatal(err)
		}
		var infos []*schema.ToolInfo
		if err := json.Unmarshal(m.requests[0].Tools, &infos); err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(infos))
		for _, info := range infos {
			names = append(names, info.Name)
		}
		return names
	}
	if got := run("tenant-a"); len(got) != 1 || got[0] != "search" {
		t.Fatalf("tenant-a tools = %v, want [search]", got)
	}
	if got := run("tenant-b"); len(got) != 1 || got[0] != "fetch" {
		t.Fatalf("tenant-b tools = %v, want [fetch]", got)
	}
}
