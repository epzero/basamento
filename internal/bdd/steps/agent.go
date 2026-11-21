package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/cucumber/godog"

	"github.com/epzero/basamento/agent"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/coretest"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/runtime"
	"github.com/epzero/basamento/tool"
)

// agentWorld drives the agent-loop scenarios over a recording, scripted
// DurableContext. Every number in a scenario is declared by its steps: the
// scripted model reports the turn's tokens, cost and run-clock time, and the
// tool simulator does the same for a call.
type agentWorld struct {
	clock *coretest.FakeClock
	defs  map[string]*agentDef
	tools map[string]*agentTool

	current *agentRun
	runs    map[string]*agentRun
	handoff json.RawMessage
}

type agentDef struct {
	name          string
	modelID       string
	system        []agent.SystemMessage
	inputTokens   int64
	caps          map[string]any
	tools         []string
	turns         []scriptedTurn
	model         *agentScriptedModel
	journal       map[string]json.RawMessage
	toolDurations map[int]time.Duration
}

type scriptedTurn struct {
	text     string
	tools    []string
	handoffs []string
	input    int64
	output   int64
	cost     int64
	estimate agent.Estimate
	duration time.Duration
}

type agentTool struct {
	result   string
	failCode string
	calls    int
}

type agentRun struct {
	agent   string
	world   *agentWorld
	dc      *agentDC
	obs     *agentObserver
	done    chan struct{}
	blocked chan struct{}
	release chan struct{}

	output agent.Output
	err    error
}

type agentObserver struct {
	reserved map[int]int64
	settled  map[int]int64
	dropped  map[int]int
}

func (o *agentObserver) Reserved(turn int, mpts int64)   { o.reserved[turn] = mpts }
func (o *agentObserver) Settled(turn int, charged int64) { o.settled[turn] = charged }
func (o *agentObserver) Dropped(turn int, messages int)  { o.dropped[turn] = messages }

func newAgentWorld() *agentWorld {
	return &agentWorld{
		clock: coretest.NewFakeClock(time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)),
		defs:  map[string]*agentDef{},
		tools: map[string]*agentTool{},
		runs:  map[string]*agentRun{},
	}
}

func (w *agentWorld) def(name string) *agentDef {
	d := w.defs[name]
	if d == nil {
		d = &agentDef{
			name:          name,
			modelID:       "script-" + name,
			journal:       map[string]json.RawMessage{},
			toolDurations: map[int]time.Duration{},
		}
		d.model = &agentScriptedModel{clock: w.clock, def: d}
		w.defs[name] = d
	}
	return d
}

func (w *agentWorld) tool(name string) *agentTool {
	t := w.tools[name]
	if t == nil {
		t = &agentTool{result: "ok"}
		w.tools[name] = t
	}
	return t
}

// run executes one agent node. stopAfter makes the fake runtime refuse one step,
// as an interrupted execution would; blockAfter parks the run after that step
// completes so a scenario can cancel it between turns.
func (w *agentWorld) run(name string, input json.RawMessage, blockAfter, stopAfter string) *agentRun {
	d := w.def(name)
	ctx, cancel := context.WithCancel(context.Background())
	dc := &agentDC{
		clock:      w.clock,
		ctx:        ctx,
		cancel:     cancel,
		journal:    d.journal,
		funcs:      map[string]runtime.StepFunc{},
		blockAfter: blockAfter,
		stopAfter:  stopAfter,
		blocked:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	r := &agentRun{
		agent:   name,
		world:   w,
		dc:      dc,
		obs:     &agentObserver{reserved: map[int]int64{}, settled: map[int]int64{}, dropped: map[int]int{}},
		done:    make(chan struct{}),
		blocked: dc.blocked,
		release: dc.release,
	}
	handoffs := make([]string, 0, len(w.defs)-1)
	for other := range w.defs {
		if other != name {
			handoffs = append(handoffs, other)
		}
	}
	sort.Strings(handoffs)
	h := agent.New(agent.Options{
		Models:   func(string) (model.Invoker, error) { return d.model, nil },
		Estimate: d.model.Estimate,
		RunTool:  w.toolRunner(d),
		Tools:    func(context.Context, string) (*tool.Set, error) { return w.toolSet(), nil },
		Defines:  func(node string) bool { _, ok := w.defs[node]; return ok },
		Register: dc.Register,
		Observer: r.obs,
	})
	go func() {
		defer close(r.done)
		res, err := h.Handle(dc, agent.Node{Name: name, Spec: d.specJSON(), Handoffs: handoffs}, input)
		r.err = err
		if len(res.Output) > 0 {
			_ = json.Unmarshal(res.Output, &r.output)
		}
	}()
	return r
}

func (w *agentWorld) runSync(name string, input json.RawMessage) *agentRun {
	r := w.run(name, input, "", "")
	<-r.done
	w.runs[name] = r
	w.current = r
	return r
}

func (w *agentWorld) toolSet() *tool.Set {
	set := &tool.Set{}
	names := make([]string, 0, len(w.tools))
	for name := range w.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		set.Tools = append(set.Tools, tool.Tool{Name: name})
	}
	return set
}

func (w *agentWorld) toolRunner(d *agentDef) tool.Runner {
	return func(_ context.Context, _ *tool.Set, call tool.Call) (tool.Outcome, error) {
		t := w.tool(call.Name)
		t.calls++
		if duration := d.toolDurations[d.model.invoked]; duration > 0 {
			w.clock.Advance(duration)
		}
		if t.failCode != "" {
			return tool.Outcome{Failed: true, Code: core.Code(t.failCode), Message: t.failCode}, nil
		}
		return tool.Outcome{Result: tool.Result{Content: json.RawMessage(fmt.Sprintf("%q", t.result))}}, nil
	}
}

func (d *agentDef) specJSON() json.RawMessage {
	spec := map[string]any{"model": d.modelID}
	if len(d.system) > 0 {
		spec["system"] = d.system
	}
	if d.inputTokens > 0 {
		spec["input_tokens"] = d.inputTokens
	}
	if len(d.caps) > 0 {
		spec["caps"] = d.caps
	}
	if len(d.tools) > 0 {
		spec["tools"] = d.tools
	}
	raw, _ := json.Marshal(spec)
	return raw
}

// --- the scripted model ----------------------------------------------------

type agentScriptedModel struct {
	clock    *coretest.FakeClock
	def      *agentDef
	invoked  int
	requests []model.Request
}

func (m *agentScriptedModel) Describe(context.Context) ([]model.Descriptor, error) {
	return []model.Descriptor{{ID: m.def.modelID}}, nil
}

func (m *agentScriptedModel) Start(_ context.Context, r model.Request) (model.Handle, error) {
	m.invoked++
	m.requests = append(m.requests, r)
	return model.Handle{ID: fmt.Sprint(m.invoked), ClientRef: r.ClientRef}, nil
}

func (m *agentScriptedModel) Events(context.Context, model.Handle, int64) (<-chan model.Event, error) {
	ch := make(chan model.Event)
	close(ch)
	return ch, nil
}

func (m *agentScriptedModel) Result(context.Context, model.Handle) (model.Result, error) {
	turn, ok := m.take()
	if !ok {
		return model.Result{}, core.NewError(core.CodeInternal, "script exhausted for "+m.def.name, nil)
	}
	if turn.duration > 0 {
		m.clock.Advance(turn.duration)
	}
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: turn.text,
		Name:    fmt.Sprintf("%s/turn-%d", m.def.name, m.invoked),
	}
	for i, name := range turn.tools {
		msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
			ID:       fmt.Sprintf("%s/turn-%d/call-%d", m.def.name, m.invoked, i+1),
			Type:     "function",
			Function: schema.FunctionCall{Name: name, Arguments: `{"q":"x"}`},
		})
	}
	for i, target := range turn.handoffs {
		msg.ToolCalls = append(msg.ToolCalls, schema.ToolCall{
			ID:       fmt.Sprintf("%s/turn-%d/call-%d", m.def.name, m.invoked, len(turn.tools)+i+1),
			Type:     "function",
			Function: schema.FunctionCall{Name: "handoff_to", Arguments: fmt.Sprintf(`{"target":%q}`, target)},
		})
	}
	raw, _ := json.Marshal(msg)
	usage := model.Usage{InputTokens: turn.input, OutputTokens: turn.output}
	return model.Result{
		State:    model.StateSucceeded,
		Message:  raw,
		Usage:    &usage,
		CostMpts: turn.cost,
	}, nil
}

func (m *agentScriptedModel) Cancel(context.Context, model.Handle) error { return nil }
func (m *agentScriptedModel) Lookup(context.Context, string) (*model.Handle, error) {
	return nil, nil
}

func (m *agentScriptedModel) Estimate(context.Context, model.Request) (agent.Estimate, error) {
	if len(m.def.turns) == 0 {
		return agent.Estimate{}, nil
	}
	return m.def.turns[0].estimate, nil
}

func (m *agentScriptedModel) take() (scriptedTurn, bool) {
	if len(m.def.turns) == 0 {
		return scriptedTurn{}, false
	}
	turn := m.def.turns[0]
	m.def.turns = m.def.turns[1:]
	return turn, true
}

// --- the fake DurableContext ----------------------------------------------

type agentDC struct {
	clock   *coretest.FakeClock
	ctx     context.Context
	cancel  context.CancelFunc
	journal map[string]json.RawMessage
	funcs   map[string]runtime.StepFunc
	ids     int

	blockAfter string
	stopAfter  string
	blocked    chan struct{}
	release    chan struct{}
	steps      []string
}

func (d *agentDC) Context() context.Context { return d.ctx }
func (d *agentDC) Now() time.Time           { return d.clock.Now() }
func (d *agentDC) NewID(kind string) string {
	d.ids++
	return fmt.Sprintf("%s_%d", kind, d.ids)
}
func (d *agentDC) Rand(string) *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }
func (d *agentDC) Child(runtime.FlowRef, any, runtime.ChildOptions) runtime.Future {
	return agentFuture{}
}
func (d *agentDC) Sleep(time.Duration) runtime.Future    { return agentFuture{} }
func (d *agentDC) Await(runtime.GateSpec) runtime.Future { return agentFuture{} }
func (d *agentDC) Select(...runtime.Future) (int, error) { return 0, nil }
func (d *agentDC) Emit(runtime.ProgressEvent) error      { return nil }
func (d *agentDC) Checkpoint(runtime.SnapshotRef) error  { return nil }
func (d *agentDC) Capabilities() runtime.Caps            { return runtime.Caps{} }

func (d *agentDC) Register(name string, fn runtime.StepFunc) error {
	if _, ok := d.funcs[name]; ok {
		return core.NewError(core.CodeConflict, "already registered", nil)
	}
	d.funcs[name] = fn
	return nil
}

// stepMatches reports whether a journal step name is the one a scenario
// names. A step name is "<node>/<visit>/<step>" while a scenario states
// "<node>/<step>": the visit's run id is not in the scenario, so it is
// dropped from the recorded name before comparing.
func stepMatches(name, want string) bool {
	if name == want {
		return true
	}
	node, rest, ok := strings.Cut(name, "/")
	if !ok {
		return false
	}
	_, step, ok := strings.Cut(rest, "/")
	if !ok {
		return false
	}
	return node+"/"+step == want
}

// journalStep finds a recorded step the way a scenario names it. The last
// recorded match wins, so a later visit to the same node is the one reported.
func (d *agentDC) journalStep(want string) (json.RawMessage, bool) {
	for i := len(d.steps) - 1; i >= 0; i-- {
		name := d.steps[i]
		if !stepMatches(name, want) {
			continue
		}
		if raw, ok := d.journal[name]; ok {
			return raw, true
		}
	}
	return nil, false
}

func (d *agentDC) Step(name string, in any, _ runtime.StepOptions) runtime.Future {
	d.steps = append(d.steps, name)
	if d.stopAfter != "" && stepMatches(name, d.stopAfter) {
		return agentFuture{err: core.NewError(core.CodeUnavailable, "interrupted", map[string]any{"step": name})}
	}
	if raw, ok := d.journal[name]; ok {
		return agentFuture{raw: raw}
	}
	fn := d.funcs[name]
	if fn == nil {
		return agentFuture{err: core.NewError(core.CodeUnsupported, "step not registered", map[string]any{"step": name})}
	}
	rawIn, err := json.Marshal(in)
	if err != nil {
		return agentFuture{err: err}
	}
	raw, err := fn(d.ctx, rawIn)
	if err != nil {
		return agentFuture{err: err}
	}
	d.journal[name] = append(json.RawMessage(nil), raw...)
	if d.blockAfter != "" && stepMatches(name, d.blockAfter) {
		close(d.blocked)
		<-d.release
	}
	return agentFuture{raw: raw}
}

type agentFuture struct {
	raw json.RawMessage
	err error
}

func (f agentFuture) Get(out any) error {
	if f.err != nil {
		return f.err
	}
	if out == nil || len(f.raw) == 0 {
		return nil
	}
	return json.Unmarshal(f.raw, out)
}
func (agentFuture) Ready() bool { return true }

// --- step definitions ------------------------------------------------------

func registerAgent(sc *godog.ScenarioContext) {
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		world(ctx).Agent = newAgentWorld()
		return ctx, nil
	})
	// Flow and agent setup.
	sc.Step(`^flow "([^"]*)" defines agent node "([^"]*)" with a scripted model$`, func(ctx context.Context, flowName, node string) error {
		world(ctx).Agent.def(node)
		return nil
	})
	sc.Step(`^flow "([^"]*)" defines agent node "([^"]*)" and agent node "([^"]*)"$`, func(ctx context.Context, flowName, first, second string) error {
		w := world(ctx).Agent
		w.def(first)
		w.def(second)
		return nil
	})
	sc.Step(`^the run clock starts at "([^"]*)"$`, func(ctx context.Context, at string) error {
		parsed, err := time.Parse(time.RFC3339, at)
		if err != nil {
			return err
		}
		world(ctx).Agent.clock = coretest.NewFakeClock(parsed)
		for _, d := range world(ctx).Agent.defs {
			d.model.clock = world(ctx).Agent.clock
		}
		return nil
	})

	// Caps.
	sc.Step(`^agent "([^"]*)" has a step cap of (\d+)$`, func(ctx context.Context, name string, n int) error {
		world(ctx).Agent.cap(name, "steps", n)
		return nil
	})
	sc.Step(`^agent "([^"]*)" has a cost cap of (\d+) milli-points$`, func(ctx context.Context, name string, n int) error {
		world(ctx).Agent.cap(name, "cost_mpts", n)
		return nil
	})
	sc.Step(`^agent "([^"]*)" has a token cap of (\d+) tokens$`, func(ctx context.Context, name string, n int) error {
		world(ctx).Agent.cap(name, "tokens", n)
		return nil
	})
	sc.Step(`^agent "([^"]*)" has a time cap of (\d+) minutes?$`, func(ctx context.Context, name string, minutes int) error {
		world(ctx).Agent.cap(name, "time", fmt.Sprintf("%dm", minutes))
		return nil
	})
	sc.Step(`^agent "([^"]*)" has no time cap$`, func(ctx context.Context, name string) error {
		delete(world(ctx).Agent.def(name).caps, "time")
		return nil
	})
	sc.Step(`^agent "([^"]*)" has a system message "([^"]*)" of (\d+) tokens and an input token limit of (\d+) tokens$`, func(ctx context.Context, name, message string, tokens, limit int) error {
		d := world(ctx).Agent.def(name)
		d.system = []agent.SystemMessage{{Content: message, Tokens: int64(tokens)}}
		d.inputTokens = int64(limit)
		return nil
	})
	sc.Step(`^agent "([^"]*)" has no system message and an input token limit of (\d+) tokens$`, func(ctx context.Context, name string, limit int) error {
		d := world(ctx).Agent.def(name)
		d.system = nil
		d.inputTokens = int64(limit)
		return nil
	})

	// Scripted model scripts.
	sc.Step(`^the scripted model of "([^"]*)" answers "([^"]*)" in (\d+) turns?$`, func(ctx context.Context, name, text string, turns int) error {
		return world(ctx).Agent.answerIn(name, text, turns)
	})
	sc.Step(`^the scripted model of "([^"]*)" answers "([^"]*)" in (\d+) turns? with an estimated cost of (\d+) milli-points and an actual cost of (\d+) milli-points$`, func(ctx context.Context, name, text string, turns int, estimate, cost int) error {
		return world(ctx).Agent.turn(name, scriptedTurn{
			text: text, cost: int64(cost), estimate: agent.Estimate{CostMpts: int64(estimate)},
		})
	})
	sc.Step(`^the scripted model of "([^"]*)" answers "([^"]*)" and requests tool "([^"]*)", then answers "([^"]*)" in (\d+) turns?$`, func(ctx context.Context, name, first, toolName, answer string, turns int) error {
		w := world(ctx).Agent
		if err := w.turn(name, scriptedTurn{text: first, tools: []string{toolName}}); err != nil {
			return err
		}
		return w.turn(name, scriptedTurn{text: answer})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)", then answers "([^"]*)" in (\d+) turns$`, func(ctx context.Context, name, toolName, answer string, turns int) error {
		w := world(ctx).Agent
		if err := w.turn(name, scriptedTurn{tools: []string{toolName}}); err != nil {
			return err
		}
		return w.turn(name, scriptedTurn{text: answer})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)" in (\d+) turns?$`, func(ctx context.Context, name, toolName string, turns int) error {
		w := world(ctx).Agent
		for i := 0; i < turns; i++ {
			if err := w.turn(name, scriptedTurn{tools: []string{toolName}}); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)" in each of (\d+) turns$`, func(ctx context.Context, name, toolName string, turns int) error {
		w := world(ctx).Agent
		for i := 0; i < turns; i++ {
			if err := w.turn(name, scriptedTurn{tools: []string{toolName}}); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)" in (\d+) turns, each reserving and costing (\d+) milli-points$`, func(ctx context.Context, name, toolName string, turns, cost int) error {
		w := world(ctx).Agent
		for i := 0; i < turns; i++ {
			if err := w.turn(name, scriptedTurn{tools: []string{toolName}, cost: int64(cost), estimate: agent.Estimate{CostMpts: int64(cost)}}); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)" in model turn (\d+) costing (\d+) milli-points, then answers "([^"]*)" in (\d+) turn costing 0$`, func(ctx context.Context, name, toolName string, turn, cost int, answer string, turns int) error {
		w := world(ctx).Agent
		if err := w.turn(name, scriptedTurn{tools: []string{toolName}, cost: int64(cost), estimate: agent.Estimate{CostMpts: int64(cost)}}); err != nil {
			return err
		}
		return w.turn(name, scriptedTurn{text: answer})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)" in (\d+) turns, each turn using (\d+) input and (\d+) output tokens$`, func(ctx context.Context, name, toolName string, turns, input, output int) error {
		w := world(ctx).Agent
		for i := 0; i < turns; i++ {
			if err := w.turn(name, scriptedTurn{tools: []string{toolName}, input: int64(input), output: int64(output), estimate: agent.Estimate{InputTokens: int64(input)}}); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)" in model turn (\d+) using (\d+) input and (\d+) output tokens, then answers "([^"]*)" in (\d+) turn costing no tokens$`, func(ctx context.Context, name, toolName string, turn, input, output int, answer string, turns int) error {
		w := world(ctx).Agent
		if err := w.turn(name, scriptedTurn{tools: []string{toolName}, input: int64(input), output: int64(output), estimate: agent.Estimate{InputTokens: int64(input)}}); err != nil {
			return err
		}
		return w.turn(name, scriptedTurn{text: answer})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)" in each of (\d+) turns, and the tool call of model turn (\d+) takes (\d+) seconds on the run clock$`, func(ctx context.Context, name, toolName string, turns, turn, seconds int) error {
		w := world(ctx).Agent
		for i := 0; i < turns; i++ {
			if err := w.turn(name, scriptedTurn{tools: []string{toolName}}); err != nil {
				return err
			}
		}
		w.def(name).toolDurations[turn] = time.Duration(seconds) * time.Second
		return nil
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)", then answers "([^"]*)" in (\d+) turns, and the tool call of model turn (\d+) takes (\d+) seconds on the run clock$`, func(ctx context.Context, name, toolName, answer string, turns, turn, seconds int) error {
		w := world(ctx).Agent
		if err := w.turn(name, scriptedTurn{tools: []string{toolName}}); err != nil {
			return err
		}
		if err := w.turn(name, scriptedTurn{text: answer}); err != nil {
			return err
		}
		w.def(name).toolDurations[turn] = time.Duration(seconds) * time.Second
		return nil
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)" in model turn (\d+) using (\d+) tokens, in model turn (\d+) using (\d+) tokens, and in model turn (\d+) using (\d+) tokens$`, func(ctx context.Context, name, toolName string, t1, n1, t2, n2, t3, n3 int) error {
		w := world(ctx).Agent
		for _, n := range []int{n1, n2, n3} {
			if err := w.turn(name, scriptedTurn{tools: []string{toolName}, input: int64(n), estimate: agent.Estimate{InputTokens: int64(n)}}); err != nil {
				return err
			}
		}
		return w.turn(name, scriptedTurn{text: "Done."})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)" in model turn (\d+) using (\d+) tokens and in model turn (\d+) using (\d+) tokens$`, func(ctx context.Context, name, toolName string, t1, n1, t2, n2 int) error {
		w := world(ctx).Agent
		for _, n := range []int{n1, n2} {
			if err := w.turn(name, scriptedTurn{tools: []string{toolName}, input: int64(n), estimate: agent.Estimate{InputTokens: int64(n)}}); err != nil {
				return err
			}
		}
		return w.turn(name, scriptedTurn{text: "Done."})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tools "([^"]*)" and "([^"]*)", then answers "([^"]*)" in (\d+) turns$`, func(ctx context.Context, name, first, second, answer string, turns int) error {
		w := world(ctx).Agent
		if err := w.turn(name, scriptedTurn{tools: []string{first, second}}); err != nil {
			return err
		}
		return w.turn(name, scriptedTurn{text: answer})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests handoffs to agents "([^"]*)" and "([^"]*)" in (\d+) turn$`, func(ctx context.Context, name, first, second string, turns int) error {
		return world(ctx).Agent.turn(name, scriptedTurn{handoffs: []string{first, second}})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tools "([^"]*)" and a handoff to agent "([^"]*)" in (\d+) turn$`, func(ctx context.Context, name, toolName, target string, turns int) error {
		return world(ctx).Agent.turn(name, scriptedTurn{tools: []string{toolName}, handoffs: []string{target}})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)", then requests a handoff to agent "([^"]*)" in (\d+) turns$`, func(ctx context.Context, name, toolName, target string, turns int) error {
		w := world(ctx).Agent
		if err := w.turn(name, scriptedTurn{tools: []string{toolName}}); err != nil {
			return err
		}
		return w.turn(name, scriptedTurn{handoffs: []string{target}})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests a handoff to agent "([^"]*)" in (\d+) turn$`, func(ctx context.Context, name, target string, turns int) error {
		return world(ctx).Agent.turn(name, scriptedTurn{handoffs: []string{target}})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests a handoff to agent "([^"]*)" in (\d+) turn costing (\d+) milli-points$`, func(ctx context.Context, name, target string, turns, cost int) error {
		return world(ctx).Agent.turn(name, scriptedTurn{handoffs: []string{target}, cost: int64(cost), estimate: agent.Estimate{CostMpts: int64(cost)}})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests a handoff to agent "([^"]*)" in (\d+) turn using (\d+) input and (\d+) output tokens$`, func(ctx context.Context, name, target string, turns, input, output int) error {
		return world(ctx).Agent.turn(name, scriptedTurn{handoffs: []string{target}, input: int64(input), output: int64(output), estimate: agent.Estimate{InputTokens: int64(input)}})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests a handoff to agent "([^"]*)" in model turn (\d+), which takes (\d+) seconds on the run clock$`, func(ctx context.Context, name, target string, turn, seconds int) error {
		return world(ctx).Agent.turn(name, scriptedTurn{handoffs: []string{target}, duration: time.Duration(seconds) * time.Second})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)" in each of (\d+) turns after the handoff$`, func(ctx context.Context, name, toolName string, turns int) error {
		w := world(ctx).Agent
		for i := 0; i < turns; i++ {
			if err := w.turn(name, scriptedTurn{tools: []string{toolName}}); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)" in each of (\d+) turns after the handoff, and its model turn (\d+) takes (\d+) seconds on the run clock$`, func(ctx context.Context, name, toolName string, turns, turn, seconds int) error {
		w := world(ctx).Agent
		for i := 0; i < turns; i++ {
			t := scriptedTurn{tools: []string{toolName}}
			if i+1 == turn {
				t.duration = time.Duration(seconds) * time.Second
			}
			if err := w.turn(name, t); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)" in (\d+) turns after the handoff, each costing (\d+) milli-points$`, func(ctx context.Context, name, toolName string, turns, cost int) error {
		w := world(ctx).Agent
		for i := 0; i < turns; i++ {
			if err := w.turn(name, scriptedTurn{tools: []string{toolName}, cost: int64(cost), estimate: agent.Estimate{CostMpts: int64(cost)}}); err != nil {
				return err
			}
		}
		return nil
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)" in (\d+) turns after the handoff, each using (\d+) input and (\d+) output tokens, then answers "([^"]*)" in (\d+) turn using (\d+) input and (\d+) output tokens$`, func(ctx context.Context, name, toolName string, turns, input, output int, answer string, answers, ain, aout int) error {
		w := world(ctx).Agent
		for i := 0; i < turns; i++ {
			if err := w.turn(name, scriptedTurn{tools: []string{toolName}, input: int64(input), output: int64(output), estimate: agent.Estimate{InputTokens: int64(input)}}); err != nil {
				return err
			}
		}
		return w.turn(name, scriptedTurn{text: answer, input: int64(ain), output: int64(aout), estimate: agent.Estimate{InputTokens: int64(ain)}})
	})
	sc.Step(`^the scripted model of "([^"]*)" requests tool "([^"]*)" in (\d+) turn after the handoff, which takes (\d+) seconds on the run clock, then answers "([^"]*)" in (\d+) turn$`, func(ctx context.Context, name, toolName string, turns, seconds int, answer string, answers int) error {
		w := world(ctx).Agent
		d := w.def(name)
		if err := w.turn(name, scriptedTurn{tools: []string{toolName}}); err != nil {
			return err
		}
		d.toolDurations[1] = time.Duration(seconds) * time.Second
		return w.turn(name, scriptedTurn{text: answer})
	})
	sc.Step(`^the scripted model of "([^"]*)" answers "([^"]*)" in (\d+) turn, which takes (\d+) seconds on the run clock$`, func(ctx context.Context, name, text string, turns, seconds int) error {
		return world(ctx).Agent.turn(name, scriptedTurn{text: text, duration: time.Duration(seconds) * time.Second})
	})

	// Tools.
	sc.Step(`^tool "([^"]*)" returns "([^"]*)"$`, func(ctx context.Context, name, result string) error {
		world(ctx).Agent.tool(name).result = result
		return nil
	})
	sc.Step(`^tool "([^"]*)" fails with code "([^"]*)"$`, func(ctx context.Context, name, code string) error {
		world(ctx).Agent.tool(name).failCode = code
		return nil
	})

	// Running.
	sc.Step(`^an agent run of "([^"]*)" runs$`, func(ctx context.Context, name string) error {
		world(ctx).Agent.runSync(name, nil)
		return nil
	})
	sc.Step(`^an agent run of "([^"]*)" completes$`, func(ctx context.Context, name string) error {
		world(ctx).Agent.runSync(name, nil)
		return nil
	})
	sc.Step(`^an agent run of "([^"]*)" reaches model turn (\d+)$`, func(ctx context.Context, name string, turn int) error {
		world(ctx).Agent.runSync(name, nil)
		return nil
	})
	sc.Step(`^an agent run of "([^"]*)" resumes from that record$`, func(ctx context.Context, name string) error {
		world(ctx).Agent.runSync(name, nil)
		return nil
	})
	sc.Step(`^the step results of model turn (\d+) and its tool call are already recorded$`, func(ctx context.Context, turn int) error {
		w := world(ctx).Agent
		// Record the first turn and its tool call by running until the next
		// turn would start, then stopping as an interrupted run does.
		r := w.run("worker", nil, "", fmt.Sprintf("worker/turn-%d", turn+1))
		<-r.done
		w.current = r
		w.runs["worker"] = r
		// The assertions after resuming count only what the resumed run does.
		w.def("worker").model.invoked = 0
		for _, t := range w.tools {
			t.calls = 0
		}
		return nil
	})
	sc.Step(`^an agent run of "([^"]*)" has completed model turn (\d+) and its tool call$`, func(ctx context.Context, name string, turn int) error {
		w := world(ctx).Agent
		r := w.run(name, nil, fmt.Sprintf("%s/tool-%d-1", name, turn), "")
		<-r.blocked
		w.current = r
		w.runs[name] = r
		return nil
	})
	sc.Step(`^the agent run is cancelled before model turn (\d+) starts$`, func(ctx context.Context, turn int) error {
		r := world(ctx).Agent.current
		r.dc.cancel()
		close(r.release)
		<-r.done
		return nil
	})
	sc.Step(`^an agent run of "([^"]*)" completed with a handoff to node "([^"]*)"$`, func(ctx context.Context, name, target string) error {
		w := world(ctx).Agent
		r := w.runSync(name, nil)
		if r.err != nil {
			return r.err
		}
		if r.output.State != agent.StateHandoff || r.output.Target != target {
			return fmt.Errorf("run state %q target %q, want handoff to %q", r.output.State, r.output.Target, target)
		}
		raw, _ := json.Marshal(r.output)
		w.handoff = raw
		return nil
	})
	sc.Step(`^an agent run of "([^"]*)" starts with that handoff$`, func(ctx context.Context, name string) error {
		w := world(ctx).Agent
		w.runSync(name, w.handoff)
		return nil
	})
	sc.Step(`^the agent runs of "([^"]*)" and then "([^"]*)" run$`, func(ctx context.Context, first, second string) error {
		return world(ctx).Agent.runPair(first, second, false)
	})
	sc.Step(`^the agent runs of "([^"]*)" and then "([^"]*)" complete$`, func(ctx context.Context, first, second string) error {
		return world(ctx).Agent.runPair(first, second, true)
	})

	// Assertions on the run.
	sc.Step(`^the agent run is completed with state "([^"]*)"$`, func(ctx context.Context, state string) error {
		return stateIs(world(ctx).Agent.current, state)
	})
	sc.Step(`^the agent run of "([^"]*)" is completed with state "([^"]*)"$`, func(ctx context.Context, name, state string) error {
		return stateIs(world(ctx).Agent.runs[name], state)
	})
	sc.Step(`^the agent run is completed with output "([^"]*)"$`, func(ctx context.Context, output string) error {
		return outputIs(world(ctx).Agent.current, output)
	})
	sc.Step(`^the agent run of "([^"]*)" is completed with output "([^"]*)"$`, func(ctx context.Context, name, output string) error {
		return outputIs(world(ctx).Agent.runs[name], output)
	})
	sc.Step(`^the agent run of "([^"]*)" is completed with state "handoff" with started_at "([^"]*)" and time cap "([^"]*)"$`, func(ctx context.Context, name, started, cap string) error {
		return handoffTiming(world(ctx).Agent.runs[name], started, cap, false)
	})
	sc.Step(`^the agent run of "([^"]*)" is completed with state "handoff" with started_at "([^"]*)" and no time cap$`, func(ctx context.Context, name, started string) error {
		return handoffTiming(world(ctx).Agent.runs[name], started, "", true)
	})
	sc.Step(`^the agent run output names node "([^"]*)"$`, func(ctx context.Context, node string) error {
		if got := world(ctx).Agent.current.output.Target; got != node {
			return fmt.Errorf("output names %q, want %q", got, node)
		}
		return nil
	})
	sc.Step(`^the agent run output holds model turns (\d+) and (\d+) in order$`, func(ctx context.Context, first, second int) error {
		return outputTurns(world(ctx).Agent.current, first, second)
	})
	sc.Step(`^the agent run output holds only model turn (\d+)$`, func(ctx context.Context, turn int) error {
		return outputOnlyTurn(world(ctx).Agent.current, turn)
	})
	sc.Step(`^the agent run has (\d+) model turn steps?$`, func(ctx context.Context, n int) error {
		return countSteps(world(ctx).Agent.current, "/turn-", n)
	})
	sc.Step(`^the agent run has (\d+) tool-call steps?$`, func(ctx context.Context, n int) error {
		return countSteps(world(ctx).Agent.current, "/tool-", n)
	})
	sc.Step(`^the result of the model turn (\d+) step is the model output "([^"]*)"$`, func(ctx context.Context, turn int, text string) error {
		r := world(ctx).Agent.current
		raw, ok := r.dc.journalStep(fmt.Sprintf("%s/turn-%d", r.agent, turn))
		if !ok {
			return fmt.Errorf("no result recorded for model turn %d", turn)
		}
		var result struct {
			Message schema.Message `json:"message"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return err
		}
		if result.Message.Content != text {
			return fmt.Errorf("model turn %d output %q, want %q", turn, result.Message.Content, text)
		}
		return nil
	})
	sc.Step(`^the scripted model received (\d+) invocations?$`, func(ctx context.Context, n int) error {
		return invocations(world(ctx).Agent.current, n)
	})
	sc.Step(`^the scripted model received (\d+) invocations? after resuming$`, func(ctx context.Context, n int) error {
		return invocations(world(ctx).Agent.current, n)
	})
	sc.Step(`^the scripted model of "([^"]*)" received (\d+) invocations?$`, func(ctx context.Context, name string, n int) error {
		if got := world(ctx).Agent.def(name).model.invoked; got != n {
			return fmt.Errorf("model of %s received %d invocations, want %d", name, got, n)
		}
		return nil
	})
	sc.Step(`^tool "([^"]*)" was invoked once$`, func(ctx context.Context, name string) error {
		if got := world(ctx).Agent.tool(name).calls; got != 1 {
			return fmt.Errorf("tool %s was invoked %d times, want 1", name, got)
		}
		return nil
	})
	sc.Step(`^tool "([^"]*)" was invoked (\d+) times?$`, func(ctx context.Context, name string, n int) error {
		if got := world(ctx).Agent.tool(name).calls; got != n {
			return fmt.Errorf("tool %s was invoked %d times, want %d", name, got, n)
		}
		return nil
	})
	sc.Step(`^tool "([^"]*)" was invoked (\d+) times? after resuming$`, func(ctx context.Context, name string, n int) error {
		if got := world(ctx).Agent.tool(name).calls; got != n {
			return fmt.Errorf("tool %s was invoked %d times after resuming, want %d", name, got, n)
		}
		return nil
	})
	sc.Step(`^the agent run fails with code "([^"]*)"$`, func(ctx context.Context, code string) error {
		return failedWith(world(ctx).Agent.current, code, "")
	})
	sc.Step(`^the agent run fails with code "([^"]*)" naming agent "([^"]*)"$`, func(ctx context.Context, code, name string) error {
		return failedWith(world(ctx).Agent.current, code, name)
	})
	sc.Step(`^the agent run of "([^"]*)" runs its own model turns (\d+) and (\d+)$`, func(ctx context.Context, name string, first, second int) error {
		return ownTurns(world(ctx).Agent.runs[name], first, second)
	})

	// Budget observation.
	sc.Step(`^(\d+) milli-points were reserved before the start of model turn (\d+)$`, func(ctx context.Context, mpts, turn int) error {
		r := world(ctx).Agent.current
		if got := r.obs.reserved[turn]; got != int64(mpts) {
			return fmt.Errorf("reserved %d milli-points for turn %d, want %d", got, turn, mpts)
		}
		return nil
	})
	sc.Step(`^model turn (\d+) was charged (\d+) milli-points and its reservation was closed$`, func(ctx context.Context, turn, mpts int) error {
		r := world(ctx).Agent.current
		if got := r.obs.settled[turn]; got != int64(mpts) {
			return fmt.Errorf("turn %d settled %d milli-points, want %d", turn, got, mpts)
		}
		return nil
	})

	// Messages.
	sc.Step(`^the messages of model turn (\d+) contain the system message "([^"]*)"$`, func(ctx context.Context, turn int, text string) error {
		return messageHasSystem(world(ctx).Agent.current, turn, text)
	})
	sc.Step(`^the messages of model turn (\d+) contain model turn (\d+)$`, func(ctx context.Context, turn, other int) error {
		return messageHasTurn(world(ctx).Agent.current, turn, other)
	})
	sc.Step(`^the messages of model turn (\d+) contain model turns (\d+) and (\d+)$`, func(ctx context.Context, turn, first, second int) error {
		r := world(ctx).Agent.current
		if err := messageHasTurn(r, turn, first); err != nil {
			return err
		}
		return messageHasTurn(r, turn, second)
	})
	sc.Step(`^the messages of model turn (\d+) do not contain model turn (\d+)$`, func(ctx context.Context, turn, other int) error {
		return messageHasNoTurn(world(ctx).Agent.current, turn, other)
	})
	sc.Step(`^the messages of model turn (\d+) contain no request for tool "([^"]*)" from model turn (\d+)$`, func(ctx context.Context, turn int, toolName string, other int) error {
		return messageHasNoRequest(world(ctx).Agent.current, turn, toolName, other)
	})
	sc.Step(`^the messages of model turn (\d+) contain no result of tool "([^"]*)" that followed model turn (\d+)$`, func(ctx context.Context, turn int, toolName string, other int) error {
		return messageHasNoResult(world(ctx).Agent.current, turn, toolName, other)
	})
	sc.Step(`^the messages of model turn (\d+) contain the result of tool "([^"]*)": "([^"]*)"$`, func(ctx context.Context, turn int, toolName, text string) error {
		r := world(ctx).Agent.current
		if err := messageHasTurn(r, turn, turn-1); err != nil {
			return err
		}
		return messageHasResult(r, r.agent, turn, turn-1, toolName, text)
	})
	sc.Step(`^the messages of model turn (\d+) contain model turn (\d+) and the result of tool "([^"]*)": "([^"]*)"$`, func(ctx context.Context, turn, other int, toolName, text string) error {
		r := world(ctx).Agent.current
		if err := messageHasTurn(r, turn, other); err != nil {
			return err
		}
		return messageHasResult(r, r.agent, turn, other, toolName, text)
	})
	sc.Step(`^the messages of model turn (\d+) of "([^"]*)" contain model turn (\d+) of "([^"]*)"$`, func(ctx context.Context, turn int, name string, other int, otherAgent string) error {
		return messageHasTurnOf(world(ctx).Agent, name, turn, otherAgent, other)
	})
	sc.Step(`^the messages of model turn (\d+) of "([^"]*)" contain model turns (\d+) and (\d+) of "([^"]*)"$`, func(ctx context.Context, turn int, name string, first, second int, otherAgent string) error {
		w := world(ctx).Agent
		if err := messageHasTurnOf(w, name, turn, otherAgent, first); err != nil {
			return err
		}
		return messageHasTurnOf(w, name, turn, otherAgent, second)
	})
	sc.Step(`^the messages of model turn (\d+) of "([^"]*)" contain the request of tool "([^"]*)" from model turn (\d+) of "([^"]*)"$`, func(ctx context.Context, turn int, name, toolName string, other int, otherAgent string) error {
		return messageHasRequestOf(world(ctx).Agent, name, turn, otherAgent, other, toolName)
	})
}

func (w *agentWorld) cap(name, key string, value any) {
	d := w.def(name)
	if d.caps == nil {
		d.caps = map[string]any{}
	}
	d.caps[key] = value
}

func (w *agentWorld) turn(name string, turn scriptedTurn) error {
	d := w.def(name)
	if turn.estimate.CostMpts == 0 && turn.cost != 0 {
		turn.estimate.CostMpts = turn.cost
	}
	d.turns = append(d.turns, turn)
	return nil
}

func (w *agentWorld) answerIn(name, text string, turns int) error {
	if turns < 1 {
		turns = 1
	}
	for i := 0; i < turns; i++ {
		t := scriptedTurn{}
		if i == turns-1 {
			t.text = text
		}
		if err := w.turn(name, t); err != nil {
			return err
		}
	}
	return nil
}

func (w *agentWorld) runPair(first, second string, wantComplete bool) error {
	r := w.runSync(first, nil)
	if r.err != nil {
		return r.err
	}
	if r.output.State != agent.StateHandoff {
		return fmt.Errorf("run of %s state %q, want handoff", first, r.output.State)
	}
	raw, _ := json.Marshal(r.output)
	w.runSync(second, raw)
	return nil
}

func stateIs(r *agentRun, state string) error {
	if r == nil {
		return fmt.Errorf("no agent run")
	}
	if r.err != nil {
		return fmt.Errorf("run failed: %w", r.err)
	}
	if r.output.State != state {
		return fmt.Errorf("run state %q, want %q", r.output.State, state)
	}
	return nil
}

func outputIs(r *agentRun, output string) error {
	if r == nil {
		return fmt.Errorf("no agent run")
	}
	if r.err != nil {
		return fmt.Errorf("run failed: %w", r.err)
	}
	if r.output.Answer != output {
		return fmt.Errorf("run output %q, want %q (state %q)", r.output.Answer, output, r.output.State)
	}
	return nil
}

func handoffTiming(r *agentRun, started, cap string, noCap bool) error {
	if err := stateIs(r, agent.StateHandoff); err != nil {
		return err
	}
	if r.output.StartedAt == nil {
		return fmt.Errorf("handoff has no started_at")
	}
	want, err := time.Parse(time.RFC3339, started)
	if err != nil {
		return err
	}
	if !r.output.StartedAt.Equal(want) {
		return fmt.Errorf("started_at %s, want %s", r.output.StartedAt.Format(time.RFC3339), started)
	}
	if noCap {
		if r.output.TimeCap != nil {
			return fmt.Errorf("time cap %v, want none", *r.output.TimeCap)
		}
		return nil
	}
	if r.output.TimeCap == nil {
		return fmt.Errorf("time cap is null, want %s", cap)
	}
	raw, _ := json.Marshal(r.output.TimeCap)
	if got := strings.Trim(string(raw), `"`); got != cap {
		return fmt.Errorf("time cap %q, want %q", got, cap)
	}
	return nil
}

func outputTurns(r *agentRun, first, second int) error {
	names, err := turnNames(r)
	if err != nil {
		return err
	}
	want := []string{fmt.Sprintf("%s/turn-%d", r.agent, first), fmt.Sprintf("%s/turn-%d", r.agent, second)}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		return fmt.Errorf("output turns %v, want %v", names, want)
	}
	return nil
}

func outputOnlyTurn(r *agentRun, turn int) error {
	names, err := turnNames(r)
	if err != nil {
		return err
	}
	want := fmt.Sprintf("%s/turn-%d", r.agent, turn)
	if len(names) != 1 || names[0] != want {
		return fmt.Errorf("output turns %v, want only %s", names, want)
	}
	return nil
}

func turnNames(r *agentRun) ([]string, error) {
	names := make([]string, 0, len(r.output.Turns))
	for _, turn := range r.output.Turns {
		var msg schema.Message
		if err := json.Unmarshal(turn.Message, &msg); err != nil {
			return nil, err
		}
		names = append(names, msg.Name)
	}
	return names, nil
}

func ownTurns(r *agentRun, first, second int) error {
	names, err := turnNames(r)
	if err != nil {
		return err
	}
	var own []string
	for _, name := range names {
		if strings.HasPrefix(name, r.agent+"/") {
			own = append(own, name)
		}
	}
	want := []string{fmt.Sprintf("%s/turn-%d", r.agent, first), fmt.Sprintf("%s/turn-%d", r.agent, second)}
	if len(own) != len(want) || own[0] != want[0] || own[1] != want[1] {
		return fmt.Errorf("own turns %v, want %v", own, want)
	}
	return nil
}

func countSteps(r *agentRun, marker string, n int) error {
	count := 0
	for _, name := range r.dc.steps {
		if strings.Contains(name, marker) {
			count++
		}
	}
	if count != n {
		return fmt.Errorf("run made %d steps matching %q, want %d", count, marker, n)
	}
	return nil
}

func invocations(r *agentRun, n int) error {
	if got := r.world.def(r.agent).model.invoked; got != n {
		return fmt.Errorf("scripted model received %d invocations, want %d", got, n)
	}
	return nil
}

func failedWith(r *agentRun, code, name string) error {
	if r == nil || r.err == nil {
		return fmt.Errorf("run did not fail, want %s", code)
	}
	if got := string(core.CodeOf(r.err)); got != code {
		return fmt.Errorf("run failed with %s, want %s: %w", got, code, r.err)
	}
	if name != "" && !strings.Contains(r.err.Error(), name) {
		return fmt.Errorf("run error does not name %q: %s", name, r.err.Error())
	}
	return nil
}

func messagesOf(w *agentWorld, agentName string, turn int) ([]*schema.Message, error) {
	d := w.def(agentName)
	if turn < 1 || turn > len(d.model.requests) {
		return nil, fmt.Errorf("agent %s made %d model turns, no turn %d", agentName, len(d.model.requests), turn)
	}
	var msgs []*schema.Message
	if err := json.Unmarshal(d.model.requests[turn-1].Messages, &msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}

func messagesOfFor(r *agentRun, turn int) ([]*schema.Message, error) {
	return messagesOf(r.world, r.agent, turn)
}

func messageHasSystem(r *agentRun, turn int, text string) error {
	msgs, err := messagesOfFor(r, turn)
	if err != nil {
		return err
	}
	for _, m := range msgs {
		if m.Role == schema.System && m.Content == text {
			return nil
		}
	}
	return fmt.Errorf("model turn %d messages do not contain system %q", turn, text)
}

func messageHasTurn(r *agentRun, turn, other int) error {
	msgs, err := messagesOfFor(r, turn)
	if err != nil {
		return err
	}
	want := fmt.Sprintf("%s/turn-%d", r.agent, other)
	for _, m := range msgs {
		if m.Name == want {
			return nil
		}
	}
	return fmt.Errorf("model turn %d messages do not contain %s", turn, want)
}

func messageHasNoTurn(r *agentRun, turn, other int) error {
	msgs, err := messagesOfFor(r, turn)
	if err != nil {
		return err
	}
	want := fmt.Sprintf("%s/turn-%d", r.agent, other)
	for _, m := range msgs {
		if m.Name == want {
			return fmt.Errorf("model turn %d messages contain %s", turn, want)
		}
	}
	return nil
}

func messageHasNoRequest(r *agentRun, turn int, toolName string, other int) error {
	msgs, err := messagesOfFor(r, turn)
	if err != nil {
		return err
	}
	want := fmt.Sprintf("%s/turn-%d", r.agent, other)
	for _, m := range msgs {
		if m.Name != want {
			continue
		}
		for _, call := range m.ToolCalls {
			if call.Function.Name == toolName {
				return fmt.Errorf("model turn %d messages contain a %s request from %s", turn, toolName, want)
			}
		}
	}
	return nil
}

func messageHasNoResult(r *agentRun, turn int, toolName string, other int) error {
	msgs, err := messagesOfFor(r, turn)
	if err != nil {
		return err
	}
	prefix := fmt.Sprintf("%s/turn-%d/", r.agent, other)
	for _, m := range msgs {
		if m.Role == schema.Tool && m.ToolName == toolName && strings.HasPrefix(m.ToolCallID, prefix) {
			return fmt.Errorf("model turn %d messages contain a %s result from %s", turn, toolName, prefix)
		}
	}
	return nil
}

func messageHasResult(r *agentRun, agentName string, turn, other int, toolName, text string) error {
	msgs, err := messagesOfFor(r, turn)
	if err != nil {
		return err
	}
	prefix := fmt.Sprintf("%s/turn-%d/", agentName, other)
	for _, m := range msgs {
		if m.Role == schema.Tool && m.ToolName == toolName && strings.HasPrefix(m.ToolCallID, prefix) && m.Content == text {
			return nil
		}
	}
	return fmt.Errorf("model turn %d messages do not contain the %s result %q from %s", turn, toolName, text, prefix)
}

func messageHasTurnOf(w *agentWorld, name string, turn int, otherAgent string, other int) error {
	msgs, err := messagesOf(w, name, turn)
	if err != nil {
		return err
	}
	want := fmt.Sprintf("%s/turn-%d", otherAgent, other)
	for _, m := range msgs {
		if m.Name == want {
			return nil
		}
	}
	return fmt.Errorf("model turn %d of %s does not contain %s", turn, name, want)
}

func messageHasRequestOf(w *agentWorld, name string, turn int, otherAgent string, other int, toolName string) error {
	msgs, err := messagesOf(w, name, turn)
	if err != nil {
		return err
	}
	want := fmt.Sprintf("%s/turn-%d", otherAgent, other)
	for _, m := range msgs {
		if m.Name != want {
			continue
		}
		for _, call := range m.ToolCalls {
			if call.Function.Name == toolName {
				return nil
			}
		}
	}
	return fmt.Errorf("model turn %d of %s does not contain the %s request from %s", turn, name, toolName, want)
}
