package conversation

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/epzero/basamento/agent"
	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/model"
	"github.com/epzero/basamento/runtime"
)

// answerModel answers every invocation with one assistant message.
type answerModel struct{ reply string }

func (answerModel) Describe(context.Context) ([]model.Descriptor, error) { return nil, nil }
func (answerModel) Start(context.Context, model.Request) (model.Handle, error) {
	return model.Handle{ID: "h", ClientRef: "client"}, nil
}
func (answerModel) Events(context.Context, model.Handle, int64) (<-chan model.Event, error) {
	ch := make(chan model.Event)
	close(ch)
	return ch, nil
}
func (m answerModel) Result(context.Context, model.Handle) (model.Result, error) {
	msg, err := json.Marshal(map[string]string{"role": "assistant", "content": m.reply})
	if err != nil {
		return model.Result{}, err
	}
	return model.Result{State: model.StateSucceeded, Message: msg}, nil
}
func (answerModel) Cancel(context.Context, model.Handle) error            { return nil }
func (answerModel) Lookup(context.Context, string) (*model.Handle, error) { return nil, nil }

// stepDC runs step functions at once, as the local runtime does, so a real
// agent.Handler can run in this package's tests.
type stepDC struct {
	ctx context.Context
	fns map[string]runtime.StepFunc
}

func newStepDC() *stepDC {
	return &stepDC{ctx: context.Background(), fns: map[string]runtime.StepFunc{}}
}

func (d *stepDC) Context() context.Context { return d.ctx }
func (d *stepDC) Now() time.Time           { return time.Unix(0, 0).UTC() }
func (d *stepDC) NewID(kind string) string { return kind }
func (d *stepDC) Rand(string) *rand.Rand   { return rand.New(rand.NewPCG(1, 2)) }
func (d *stepDC) Register(name string, fn runtime.StepFunc) error {
	d.fns[name] = fn
	return nil
}
func (d *stepDC) Step(name string, in any, _ runtime.StepOptions) runtime.Future {
	raw, err := json.Marshal(in)
	if err != nil {
		return rawFuture{err: err}
	}
	fn := d.fns[name]
	if fn == nil {
		return rawFuture{err: core.NewError(core.CodeUnsupported, "test: step not registered", nil)}
	}
	out, err := fn(d.ctx, raw)
	return rawFuture{raw: out, err: err}
}
func (stepDC) Child(runtime.FlowRef, any, runtime.ChildOptions) runtime.Future {
	return rawFuture{err: core.NewError(core.CodeUnsupported, "test: no child", nil)}
}
func (stepDC) Sleep(time.Duration) runtime.Future    { return rawFuture{} }
func (stepDC) Await(runtime.GateSpec) runtime.Future { return rawFuture{} }
func (stepDC) Select(...runtime.Future) (int, error) { return 0, nil }
func (stepDC) Emit(runtime.ProgressEvent) error      { return nil }
func (stepDC) Checkpoint(runtime.SnapshotRef) error  { return nil }
func (stepDC) Capabilities() runtime.Caps            { return runtime.Caps{} }

type rawFuture struct {
	raw json.RawMessage
	err error
}

func (f rawFuture) Get(out any) error {
	if f.err != nil {
		return f.err
	}
	if out == nil || len(f.raw) == 0 {
		return nil
	}
	return json.Unmarshal(f.raw, out)
}
func (rawFuture) Ready() bool { return true }

// TestRunRecordsARealAgentTurn feeds the coordinator the state a real
// agent.Handler returns through a flow run — the flow's input state with the
// agent output merged under its out key — and checks the record. The carried
// user message must not become a model message twice, the agent's answer must
// be recorded, and the turn must complete (REQ-CONV-012).
func TestRunRecordsARealAgentTurn(t *testing.T) {
	history := TurnInput{Messages: []MessageRecord{{Role: RoleUser, Text: "Hello"}}}
	input := turnFlowInput(history)

	dc := newStepDC()
	handler := agent.New(agent.Options{
		Models:   func(string) (model.Invoker, error) { return answerModel{reply: "Hi Ana"}, nil },
		Register: dc.Register,
	})
	node := agent.Node{Name: "assistant", Spec: json.RawMessage(`{"agent":"assistant","model":"script"}`)}
	res, err := handler.Handle(dc, node, input)
	if err != nil {
		t.Fatal(err)
	}

	// The flow's final state is its input state with the agent node's output
	// merged under its out key (flow/machine reduce).
	var state map[string]json.RawMessage
	if err := json.Unmarshal(input, &state); err != nil {
		t.Fatal(err)
	}
	state["out"] = res.Output
	childState, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}

	in := newInput()
	in.TurnOut = "out"
	out, err := New(Options{}).Run(newTestDC(msgEvent("m1", "Hello"), coordEvent{raw: childState}), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Record.Turns) != 1 || out.Record.Turns[0].State != TurnCompleted || out.Record.Turns[0].Code != "" {
		t.Fatalf("turn = %+v, want completed with no code", out.Record.Turns)
	}
	want := []MessageRecord{
		{Turn: 1, Role: RoleUser, Text: "Hello", ID: "m1", Status: StatusComplete},
		{Turn: 1, Role: RoleModel, Text: "Hi Ana", Position: 1, Status: StatusComplete},
	}
	if len(out.Record.Messages) != len(want) {
		t.Fatalf("messages = %+v, want %+v", out.Record.Messages, want)
	}
	for i, w := range want {
		got := out.Record.Messages[i]
		if got.Turn != w.Turn || got.Role != w.Role || got.Text != w.Text || got.Position != w.Position || got.Status != w.Status {
			t.Fatalf("message %d = %+v, want %+v", i, got, w)
		}
	}
}

func TestDecodeTurnResultNeedsTheOutKeyForAFlowState(t *testing.T) {
	raw := json.RawMessage(`{"state":"handoff","turns":[],"out":{"state":"completed"}}`)
	if _, err := decodeTurnResult(raw, "", 0); core.CodeOf(err) != core.CodeInternal {
		t.Fatalf("decode error = %v, want internal", err)
	}
}

func TestRunChildIDsDoNotCollideWithRunIDs(t *testing.T) {
	dc := newTestDC(msgEvent("m1", "Hello"), turnEvent(TurnCompleted, "Hi"))
	in := newInput()
	in.ID = "c1-turn-1" // RunID(in.ID) = conv-c1-turn-1, the old turn-1 child id of c1
	if _, err := New(Options{}).Run(dc, in); err != nil {
		t.Fatal(err)
	}
	if len(dc.children) != 1 {
		t.Fatalf("children = %d, want 1", len(dc.children))
	}
	if got := dc.children[0].id; got == RunID(in.ID) || got != "turn@c1-turn-1@1" {
		t.Fatalf("child id %q: want turn@c1-turn-1@1, not the conversation run id %q", got, RunID(in.ID))
	}
}
