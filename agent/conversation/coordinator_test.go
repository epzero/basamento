package conversation

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/epzero/basamento/runtime"
)

type coordEvent struct {
	msg  *Message
	turn *TurnOutput
}

type testDC struct {
	ctx          context.Context
	cancel       context.CancelFunc
	events       []coordEvent
	selected     *coordEvent
	children     int
	checkpoints  int
	capabilities runtime.Caps
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

func (d *testDC) Step(string, any, runtime.StepOptions) runtime.Future {
	return testFuture{dc: d, role: "step"}
}
func (d *testDC) Child(runtime.FlowRef, any, runtime.ChildOptions) runtime.Future {
	d.children++
	return testFuture{dc: d, role: "child"}
}
func (d *testDC) Sleep(time.Duration) runtime.Future    { return testFuture{dc: d, role: "step"} }
func (d *testDC) Await(runtime.GateSpec) runtime.Future { return testFuture{dc: d, role: "inbox"} }
func (d *testDC) Select(fs ...runtime.Future) (int, error) {
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
	ev := f.dc.selected
	if ev == nil {
		popped, err := f.dc.pop()
		if err != nil {
			return err
		}
		ev = &popped
	}
	f.dc.selected = nil
	switch {
	case ev.msg != nil:
		raw, _ := json.Marshal(ev.msg)
		if out == nil {
			return nil
		}
		return json.Unmarshal(raw, out)
	default:
		raw, _ := json.Marshal(ev.turn)
		if out == nil {
			return nil
		}
		return json.Unmarshal(raw, out)
	}
}
func (testFuture) Ready() bool { return true }

type recordingTurns struct {
	steers  []string
	cancels int
}

func (r *recordingTurns) Steer(_ context.Context, _ runtime.RunRef, text string) error {
	r.steers = append(r.steers, text)
	return nil
}
func (r *recordingTurns) Cancel(context.Context, runtime.RunRef, string) error {
	r.cancels++
	return nil
}

func msgEvent(id, text string) coordEvent {
	return coordEvent{msg: &Message{ID: id, Text: text}}
}

func turnEvent(state, text string) coordEvent {
	return coordEvent{turn: &TurnOutput{State: state, Messages: []ModelMessage{{Text: text}}}}
}

func TestRunStartsOneChildPerTurn(t *testing.T) {
	dc := newTestDC(
		msgEvent("m1", "Hello"), turnEvent(TurnCompleted, "Hi Ana"),
		msgEvent("m2", "And a flow?"), turnEvent(TurnCompleted, "A unit of execution"),
	)
	turns := &recordingTurns{}
	c := New(Options{Turns: turns})
	out, err := c.Run(dc, Input{Tenant: "acme", User: "ana", ID: "c1", Flow: runtime.FlowRef{Name: "assistant"}})
	if err != nil {
		t.Fatal(err)
	}
	if dc.children != 2 {
		t.Fatalf("children = %d, want 2", dc.children)
	}
	if len(out.Record.Turns) != 2 || len(out.Record.Messages) != 4 {
		t.Fatalf("record = %+v, want 2 turns and 4 messages", out.Record)
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
	out, err := c.Run(dc, Input{Tenant: "acme", User: "ana", ID: "c1", Flow: runtime.FlowRef{Name: "assistant"}})
	if err != nil {
		t.Fatal(err)
	}
	if dc.children != 2 {
		t.Fatalf("children = %d, want 2", dc.children)
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
		msgEvent("m1", "Write"), coordEvent{msg: &Message{ID: "m2", Text: "About the sea", Mode: ModeSteer}},
		turnEvent(TurnCompleted, "Done"),
	)
	turns := &recordingTurns{}
	c := New(Options{Turns: turns})
	out, err := c.Run(dc, Input{Tenant: "acme", User: "ana", ID: "c1", Flow: runtime.FlowRef{Name: "assistant"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(turns.steers) != 1 || turns.steers[0] != "About the sea" {
		t.Fatalf("steers = %v", turns.steers)
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
		msgEvent("m1", "Write"), coordEvent{msg: &Message{ID: "m2", Text: "Stop", Mode: ModeCancel}},
		coordEvent{turn: &TurnOutput{State: TurnCancelled, Code: "cancelled", Messages: []ModelMessage{{Text: "Partial", Status: StatusInterrupted}}}},
	)
	turns = &recordingTurns{}
	c = New(Options{Turns: turns})
	out, err = c.Run(dc, Input{Tenant: "acme", User: "ana", ID: "c1", Flow: runtime.FlowRef{Name: "assistant"}})
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
	out, err := c.Run(dc, Input{Tenant: "acme", User: "ana", ID: "c1", Flow: runtime.FlowRef{Name: "assistant"}, Threshold: 2})
	if err != nil {
		t.Fatal(err)
	}
	if dc.checkpoints == 0 {
		t.Fatal("no checkpoint at the threshold")
	}
	if out.Record.Continued == 0 {
		t.Fatal("the record did not count a continuation")
	}
	if got := RunID("c1"); got != "conv-c1" {
		t.Fatalf("RunID = %q, want conv-c1", got)
	}
}
