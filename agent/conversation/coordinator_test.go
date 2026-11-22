package conversation

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/runtime"
)

type coordEvent struct {
	msg  *Message
	turn *TurnOutput
	err  error
}

type childStart struct {
	id    string
	flow  runtime.FlowRef
	input json.RawMessage
}

type testDC struct {
	ctx          context.Context
	cancel       context.CancelFunc
	events       []coordEvent
	selected     *coordEvent
	children     []childStart
	steps        []string
	checkpoints  int
	capabilities runtime.Caps
	// validateIDs makes the context reject a child id containing "/", as the
	// temporal runtime does (runtime/temporal/ids.go).
	validateIDs  bool
	invalidChild error
}

func newTestDC(events ...coordEvent) *testDC {
	ctx, cancel := context.WithCancel(context.Background())
	return &testDC{ctx: ctx, cancel: cancel, events: events}
}

func (d *testDC) pop() (coordEvent, error) {
	if len(d.events) == 0 {
		d.cancel()
		return coordEvent{}, context.Canceled
	}
	ev := d.events[0]
	d.events = d.events[1:]
	return ev, nil
}

func (d *testDC) Context() context.Context { return d.ctx }
func (d *testDC) Now() time.Time           { return time.Time{} }
func (d *testDC) NewID(kind string) string { return kind }
func (d *testDC) Rand(string) *rand.Rand   { return rand.New(rand.NewPCG(1, 2)) }

func (d *testDC) Step(name string, _ any, _ runtime.StepOptions) runtime.Future {
	d.steps = append(d.steps, name)
	return testFuture{dc: d, role: "step"}
}
func (d *testDC) Child(flow runtime.FlowRef, in any, o runtime.ChildOptions) runtime.Future {
	raw, _ := json.Marshal(in)
	d.children = append(d.children, childStart{id: o.ID, flow: flow, input: raw})
	if d.validateIDs && strings.ContainsRune(o.ID, '/') {
		d.invalidChild = core.NewError(core.CodeInvalidArgument,
			"test: child id cannot contain \"/\"", map[string]any{"child": o.ID})
	}
	return testFuture{dc: d, role: "child"}
}
func (d *testDC) Sleep(time.Duration) runtime.Future    { return testFuture{dc: d, role: "step"} }
func (d *testDC) Await(runtime.GateSpec) runtime.Future { return testFuture{dc: d, role: "inbox"} }
func (d *testDC) Select(fs ...runtime.Future) (int, error) {
	if d.invalidChild != nil {
		return 1, nil
	}
	ev, err := d.pop()
	if err != nil {
		return 0, err
	}
	d.selected = &ev
	if ev.msg != nil {
		return 0, nil
	}
	return 1, nil
}
func (d *testDC) Emit(runtime.ProgressEvent) error     { return nil }
func (d *testDC) Checkpoint(runtime.SnapshotRef) error { d.checkpoints++; return nil }
func (d *testDC) Capabilities() runtime.Caps           { return d.capabilities }

type testFuture struct {
	dc   *testDC
	role string
}

func (f testFuture) Get(out any) error {
	if f.role == "step" {
		return nil
	}
	if f.role == "child" && f.dc.invalidChild != nil {
		return f.dc.invalidChild
	}
	ev := f.dc.selected
	if ev == nil {
		popped, err := f.dc.pop()
		if err != nil {
			return err
		}
		ev = &popped
	}
	f.dc.selected = nil
	if ev.err != nil {
		return ev.err
	}
	if ev.msg != nil {
		raw, _ := json.Marshal(ev.msg)
		if out == nil {
			return nil
		}
		return json.Unmarshal(raw, out)
	}
	raw, _ := json.Marshal(ev.turn)
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}
func (testFuture) Ready() bool { return true }

type recordingTurns struct {
	steers   []string
	steerRef []runtime.RunRef
	cancels  int
	cancelRe []runtime.RunRef
	partial  []ModelMessage
}

func (r *recordingTurns) Steer(_ context.Context, ref runtime.RunRef, text string) error {
	r.steers = append(r.steers, text)
	r.steerRef = append(r.steerRef, ref)
	return nil
}
func (r *recordingTurns) Cancel(_ context.Context, ref runtime.RunRef, _ string) error {
	r.cancels++
	r.cancelRe = append(r.cancelRe, ref)
	return nil
}
func (r *recordingTurns) Partial(context.Context, runtime.RunRef) ([]ModelMessage, error) {
	return r.partial, nil
}

func msgEvent(id, text string) coordEvent {
	return coordEvent{msg: &Message{ID: id, Tenant: "acme", User: "ana", Text: text}}
}

func turnEvent(state, text string) coordEvent {
	return coordEvent{turn: &TurnOutput{State: state, Messages: []ModelMessage{{Text: text}}}}
}

func newInput() Input {
	return Input{
		Tenant: "acme", User: "ana", ID: "c1",
		Flow:     runtime.FlowRef{Name: "assistant"},
		TurnFlow: runtime.FlowRef{Name: "turn"},
	}
}

func TestRunStartsOneChildPerTurn(t *testing.T) {
	dc := newTestDC(
		msgEvent("m1", "Hello"), turnEvent(TurnCompleted, "Hi Ana"),
		msgEvent("m2", "And a flow?"), turnEvent(TurnCompleted, "A unit of execution"),
	)
	turns := &recordingTurns{}
	c := New(Options{Turns: turns})
	out, err := c.Run(dc, newInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(dc.children) != 2 {
		t.Fatalf("children = %d, want 2", len(dc.children))
	}
	if len(out.Record.Turns) != 2 || len(out.Record.Messages) != 4 {
		t.Fatalf("record = %+v, want 2 turns and 4 messages", out.Record)
	}
	for i, child := range dc.children {
		if out.Record.Turns[i].Child != child.id {
			t.Fatalf("turn %d child = %q, want %q", i+1, out.Record.Turns[i].Child, child.id)
		}
	}
	want := []string{"Hello", "Hi Ana", "And a flow?", "A unit of execution"}
	for i, w := range want {
		if out.Record.Messages[i].Text != w {
			t.Fatalf("messages[%d] = %q, want %q", i, out.Record.Messages[i].Text, w)
		}
	}
}

func TestRunQueuesMessagesDuringATurn(t *testing.T) {
	dc := newTestDC(
		msgEvent("m1", "Write"), msgEvent("m2", "Then"),
		turnEvent(TurnCompleted, "First"), turnEvent(TurnCompleted, "Second"),
	)
	c := New(Options{})
	out, err := c.Run(dc, newInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(dc.children) != 2 {
		t.Fatalf("children = %d, want 2", len(dc.children))
	}
	if len(out.Record.Queue) != 0 {
		t.Fatalf("queue = %+v, want empty", out.Record.Queue)
	}
	last, _ := out.Record.LastTurn()
	if last.Index != 2 {
		t.Fatalf("last turn = %d, want 2", last.Index)
	}
}

func TestRunSteersAndCancelsTheRunningChild(t *testing.T) {
	dc := newTestDC(
		msgEvent("m1", "Write"), coordEvent{msg: &Message{ID: "m2", Tenant: "acme", User: "ana", Text: "About the sea", Mode: ModeSteer}},
		turnEvent(TurnCompleted, "Done"),
	)
	turns := &recordingTurns{}
	c := New(Options{Turns: turns})
	out, err := c.Run(dc, newInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(turns.steers) != 1 || turns.steers[0] != "About the sea" {
		t.Fatalf("steers = %v", turns.steers)
	}
	if len(turns.steerRef) != 1 || turns.steerRef[0].RunID != "conv-c1-turn-1" {
		t.Fatalf("steer refs = %+v", turns.steerRef)
	}
	found := false
	for _, m := range out.Record.Messages {
		if m.Role == RoleUser && m.Text == "About the sea" {
			found = true
		}
	}
	if !found {
		t.Fatal("the steering message is not in the history")
	}

	dc = newTestDC(
		msgEvent("m1", "Write"), coordEvent{msg: &Message{ID: "m2", Tenant: "acme", User: "ana", Text: "Stop", Mode: ModeCancel}},
		coordEvent{turn: &TurnOutput{State: TurnCancelled, Code: "cancelled", Messages: []ModelMessage{{Text: "Partial", Status: StatusInterrupted}}}},
	)
	turns = &recordingTurns{}
	c = New(Options{Turns: turns})
	out, err = c.Run(dc, newInput())
	if err != nil {
		t.Fatal(err)
	}
	if turns.cancels != 1 {
		t.Fatalf("cancels = %d, want 1", turns.cancels)
	}
	turn, _ := out.Record.LastTurn()
	if turn.State != TurnCancelled {
		t.Fatalf("turn = %+v, want cancelled", turn)
	}
}

func TestRunCheckpointsAtATurnBoundary(t *testing.T) {
	dc := newTestDC(
		msgEvent("m1", "Hello"), turnEvent(TurnCompleted, "Hi"),
		msgEvent("m2", "Again"), turnEvent(TurnCompleted, "Hi"),
	)
	dc.capabilities = runtime.Caps{ContinueAsNew: true}
	c := New(Options{})
	in := newInput()
	in.Threshold = 2
	_, err := c.Run(dc, in)
	if err != nil {
		t.Fatal(err)
	}
	if dc.checkpoints == 0 {
		t.Fatal("no checkpoint at the threshold")
	}
	if got := RunID("c1"); got != "conv-c1" {
		t.Fatalf("RunID = %q, want conv-c1", got)
	}
}

func TestRunUsesSlashFreeChildIDs(t *testing.T) {
	dc := newTestDC(
		msgEvent("m1", "Write"),
		coordEvent{msg: &Message{ID: "m2", Tenant: "acme", User: "ana", Text: "About the sea", Mode: ModeSteer}},
		coordEvent{msg: &Message{ID: "m3", Tenant: "acme", User: "ana", Text: "Stop", Mode: ModeCancel}},
		coordEvent{err: core.NewError(core.CodeCancelled, "turn cancelled", nil)},
	)
	dc.validateIDs = true
	turns := &recordingTurns{partial: []ModelMessage{{Text: "Part", Status: StatusInterrupted}}}
	c := New(Options{Turns: turns})
	out, err := c.Run(dc, newInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(dc.children) != 1 {
		t.Fatalf("children = %d, want 1", len(dc.children))
	}
	turn, ok := out.Record.LastTurn()
	if !ok || turn.State != TurnCancelled {
		t.Fatalf("turn = %+v, want cancelled on a runtime that validates child ids", turn)
	}
	if turn.Child != dc.children[0].id {
		t.Fatalf("turn child = %q, want the started child %q", turn.Child, dc.children[0].id)
	}
	last := out.Record.Messages[len(out.Record.Messages)-1]
	if last.Role != RoleModel || last.Text != "Part" || last.Status != StatusInterrupted {
		t.Fatalf("messages = %+v, want the cancelled turn's partial as interrupted", out.Record.Messages)
	}
	for _, child := range dc.children {
		if strings.ContainsRune(child.id, '/') {
			t.Fatalf("child id %q contains a slash", child.id)
		}
	}
	for _, ref := range append(turns.steerRef, turns.cancelRe...) {
		if strings.ContainsRune(ref.RunID, '/') {
			t.Fatalf("turn ref %q contains a slash", ref.RunID)
		}
	}
}

func TestRunRefusesAnotherPrincipalsMessage(t *testing.T) {
	dc := newTestDC(coordEvent{msg: &Message{ID: "m1", Tenant: "acme", User: "ben", Text: "Hello"}})
	c := New(Options{})
	out, err := c.Run(dc, newInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(dc.children) != 0 {
		t.Fatalf("%d child runs started for a refused message", len(dc.children))
	}
	if len(out.Record.Turns) != 0 || len(out.Record.Queue) != 0 {
		t.Fatalf("record changed for a refused message: %+v", out.Record)
	}
	found := false
	for _, name := range dc.steps {
		if strings.HasPrefix(name, "refuse-") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the refusal was not journaled: %v", dc.steps)
	}
}

func TestRunRefusesSteerWithoutATurn(t *testing.T) {
	dc := newTestDC(coordEvent{msg: &Message{ID: "m1", Tenant: "acme", User: "ana", Text: "Shorter", Mode: ModeSteer}})
	c := New(Options{})
	out, err := c.Run(dc, newInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(dc.children) != 0 {
		t.Fatalf("%d child runs started", len(dc.children))
	}
	if len(out.Record.Handled) != 0 {
		t.Fatalf("refused steering id was handled: %v", out.Record.Handled)
	}
}

func TestRunRecordsAFailedTurnsPartial(t *testing.T) {
	dc := newTestDC(
		msgEvent("m1", "Look up"),
		coordEvent{err: core.NewError(core.Code("vendor_error"), "upstream failed", nil)},
	)
	turns := &recordingTurns{partial: []ModelMessage{{Text: "Searching", Status: StatusInterrupted}}}
	c := New(Options{Turns: turns})
	out, err := c.Run(dc, newInput())
	if err != nil {
		t.Fatal(err)
	}
	turn, _ := out.Record.LastTurn()
	if turn.State != TurnFailed || turn.Code != "vendor_error" {
		t.Fatalf("turn = %+v, want failed vendor_error", turn)
	}
	if len(out.Record.Messages) != 2 || out.Record.Messages[1].Text != "Searching" || out.Record.Messages[1].Status != StatusInterrupted {
		t.Fatalf("record = %+v, want the partial marked interrupted", out.Record.Messages)
	}
}

func TestRunStartsWithTheTurnFlowAndItsHistory(t *testing.T) {
	dc := newTestDC(msgEvent("m1", "Hello"))
	c := New(Options{})
	_, err := c.Run(dc, newInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(dc.children) != 1 {
		t.Fatalf("children = %d, want 1", len(dc.children))
	}
	if dc.children[0].flow.Name != "turn" {
		t.Fatalf("child flow = %q, want the turn flow", dc.children[0].flow.Name)
	}
	var in struct {
		State string `json:"state"`
		Turns []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(dc.children[0].input, &in); err != nil {
		t.Fatal(err)
	}
	if in.State != "handoff" || len(in.Turns) != 1 {
		t.Fatalf("turn flow input = %s, want a handoff with one message", dc.children[0].input)
	}
	if in.Turns[0].Message.Role != "user" || in.Turns[0].Message.Content != "Hello" {
		t.Fatalf("turn flow message = %+v, want the user's Hello", in.Turns[0].Message)
	}
}
