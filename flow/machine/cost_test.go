package machine

import (
	"encoding/json"
	"math"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

func verifyPayload() json.RawMessage { return json.RawMessage(`0`) }

func assertCostMptsRejected(t *testing.T, held flow.Snapshot, ev flow.Event, wantReason string) {
	t.Helper()
	m := requireMachine(t, triageFlow())
	before, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = m.Apply(held, ev)
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("event %+v: error %v, want invalid_argument", ev, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "cost_mpts") {
		t.Fatalf("error %q, want cost_mpts", msg)
	}
	if !strings.Contains(msg, wantReason) {
		t.Fatalf("error %q, want %q", msg, wantReason)
	}
	after, err := json.Marshal(held)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("input snapshot changed after rejected cost_mpts")
	}
}

func TestCostMptsRejectsInvalidValues(t *testing.T) {
	m := requireMachine(t, triageFlow())
	held, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	assertCostMptsRejected(t, held, flow.Event{
		Type: "node_completed", Node: "verify", Payload: verifyPayload(), CostMpts: -1,
	}, "negative")
	assertCostMptsRejected(t, held, flow.Event{
		Type: "node_failed", Node: "verify", Payload: json.RawMessage(`{"code":"x"}`), CostMpts: -1,
	}, "negative")

	held.Cost = math.MaxInt64
	assertCostMptsRejected(t, held, flow.Event{
		Type: "node_completed", Node: "verify", Payload: verifyPayload(), CostMpts: 1,
	}, "overflow")

	held, _, err = m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	held.Cost = math.MaxInt64
	assertCostMptsRejected(t, held, flow.Event{
		Type: "node_failed", Node: "verify", Payload: json.RawMessage(`{"code":"x"}`), CostMpts: 1,
	}, "overflow")
}

func TestCostMptsRejectsWrongEventTypes(t *testing.T) {
	m := requireMachine(t, triageFlow())
	held, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	cannot := "cannot carry cost_mpts"
	for _, ev := range []flow.Event{
		{Type: "gate_resolved", Node: "verify", CostMpts: 1},
		{Type: "timer_fired", Node: "verify", CostMpts: 1},
		{Type: "cancelled", Node: "verify", CostMpts: 1},
	} {
		assertCostMptsRejected(t, held, ev, cannot)
	}

	gateM, gateSnap := startGate(t)
	assertCostMptsRejected(t, gateSnap, flow.Event{Type: "gate_resolved", Node: "g", CostMpts: 1}, cannot)
	_ = gateM

	waitM, waitSnap := startWait(t)
	assertCostMptsRejected(t, waitSnap, flow.Event{Type: "timer_fired", Node: "w", CostMpts: 1}, cannot)
	_ = waitM
}

func TestCostMptsAccruesOnNodeFailed(t *testing.T) {
	m := requireMachine(t, triageFlow())
	held, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	next, commands, err := m.Apply(held, flow.Event{
		Type:     "node_failed",
		Node:     "verify",
		CostMpts: 7,
		Payload:  json.RawMessage(`{"code":"x"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Cost != 7 {
		t.Fatalf("cost %d, want 7", next.Cost)
	}
	if len(commands) != 1 || commands[0].Type != "fail" {
		t.Fatalf("commands %+v, want fail", commands)
	}
	if !next.Steps[0].Done {
		t.Fatal("pending step should be marked done")
	}
}

func TestNodeFailedCostAccruesOnceForPendingStep(t *testing.T) {
	m := requireMachine(t, triageFlow())
	held, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	ev := flow.Event{
		Type:     "node_failed",
		Node:     "verify",
		CostMpts: 50,
		Payload:  json.RawMessage(`{"code":"x"}`),
	}
	snapshot, commands, err := m.Apply(held, ev)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Cost != 50 {
		t.Fatalf("cost %d, want 50", snapshot.Cost)
	}
	if commands[0].Type != "fail" {
		t.Fatalf("commands %+v, want fail", commands)
	}
	again, commands2, err := m.Apply(snapshot, ev)
	if err != nil {
		t.Fatal(err)
	}
	if again.Cost != 50 {
		t.Fatalf("duplicate apply cost %d, want 50", again.Cost)
	}
	if commands2[0].Type != "fail" {
		t.Fatalf("second commands %+v, want fail", commands2)
	}
}

func TestStaleNodeFailedDoesNotAccrueCost(t *testing.T) {
	m := requireMachine(t, triageFlow())
	held, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := m.Apply(held, flow.Event{
		Type:     "node_completed",
		Node:     "verify",
		Payload:  verifyPayload(),
		CostMpts: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	next, commands, err := m.Apply(snapshot, flow.Event{
		Type:     "node_failed",
		Node:     "verify",
		CostMpts: 99,
		Payload:  json.RawMessage(`{"code":"x"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Cost != 10 {
		t.Fatalf("cost %d, want 10", next.Cost)
	}
	if commands[0].Type != "fail" {
		t.Fatalf("commands %+v, want fail", commands)
	}
}

func TestNodeFailedForUnknownNodeDoesNotAccrueCost(t *testing.T) {
	m := requireMachine(t, triageFlow())
	held, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	next, commands, err := m.Apply(held, flow.Event{
		Type:     "node_failed",
		Node:     "missing",
		CostMpts: 50,
		Payload:  json.RawMessage(`{"code":"x"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Cost != 0 {
		t.Fatalf("cost %d, want 0", next.Cost)
	}
	if commands[0].Type != "fail" {
		t.Fatalf("commands %+v, want fail", commands)
	}
}

func TestMapAttemptNodeFailedAccruesCostOnce(t *testing.T) {
	def := mapFanoutFlow(3)
	m := requireMachine(t, def)
	held, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	next, commands, err := m.Apply(held, flow.Event{
		Type:     "node_failed",
		Node:     "work",
		Attempt:  2,
		CostMpts: 25,
		Payload:  json.RawMessage(`{"code":"x"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Cost != 25 {
		t.Fatalf("cost %d, want 25", next.Cost)
	}
	if commands[0].Type != "fail" {
		t.Fatalf("commands %+v, want fail", commands)
	}
}

func TestCostMptsAcceptsMaxInt64OnNodeCompleted(t *testing.T) {
	m := requireMachine(t, triageFlow())
	held, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	next, _, err := m.Apply(held, flow.Event{
		Type:     "node_completed",
		Node:     "verify",
		Payload:  verifyPayload(),
		CostMpts: math.MaxInt64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.Cost != math.MaxInt64 {
		t.Fatalf("cost %d, want MaxInt64", next.Cost)
	}
}

func TestCostGuardBudgetBoundary(t *testing.T) {
	for _, tc := range []struct {
		name           string
		guard          int64
		wantWorkRuns   int
		wantCostAtFail int64
	}{
		{"reached at third entry", 80, 2, 80},
		{"third visit still runs", 81, 3, 120},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := costLoopDefinition(tc.guard)
			m := requireMachine(t, def)
			snapshot, commands, err := m.Start(json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			workRuns := 0
			const maxSteps = 20
			for step := 0; len(commands) != 1 || commands[0].Type != "fail"; step++ {
				if step >= maxSteps {
					t.Fatalf("loop did not fail within %d steps; commands %+v", maxSteps, commands)
				}
				if len(commands) == 0 {
					t.Fatal("no commands to execute")
				}
				switch commands[0].Node {
				case "work":
					workRuns++
					snapshot, commands, err = m.Apply(snapshot, flow.Event{
						Type:     "node_completed",
						Node:     "work",
						CostMpts: 40,
					})
				case "check":
					snapshot, commands, err = m.Apply(snapshot, flow.Event{Type: "node_completed", Node: "check"})
				default:
					t.Fatalf("unexpected commands %+v", commands)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if code := commandCode(t, commands[0]); code != string(core.CodeBudgetExceeded) {
				t.Fatalf("code %q, want budget_exceeded", code)
			}
			if workRuns != tc.wantWorkRuns {
				t.Fatalf("completed work %d times, want %d", workRuns, tc.wantWorkRuns)
			}
			if snapshot.Cost != tc.wantCostAtFail {
				t.Fatalf("cost %d, want %d at guard failure", snapshot.Cost, tc.wantCostAtFail)
			}
		})
	}
}

func costLoopDefinition(costGuard int64) *flow.IR {
	return &flow.IR{
		Name:    "cost-loop",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"work":  {Kind: "task", Guard: &flow.Guard{CostMpts: costGuard}},
			"check": {Kind: "task"},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "work"},
			{From: "work", To: "check"},
			{From: "check", To: "work"},
		},
	}
}

func TestCostGuardFailsAfterBudgetIsSpent(t *testing.T) {
	m := requireMachine(t, costLoopDefinition(100))
	snapshot, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	workRuns := 0
	const maxSteps = 20
	for step := 0; len(commands) != 1 || commands[0].Type != "fail"; step++ {
		if step >= maxSteps {
			t.Fatalf("loop did not fail within %d steps; commands %+v", maxSteps, commands)
		}
		if len(commands) == 0 {
			t.Fatal("no commands to execute")
		}
		switch commands[0].Node {
		case "work":
			workRuns++
			snapshot, commands, err = m.Apply(snapshot, flow.Event{
				Type:     "node_completed",
				Node:     "work",
				CostMpts: 40,
			})
		case "check":
			snapshot, commands, err = m.Apply(snapshot, flow.Event{Type: "node_completed", Node: "check"})
		default:
			t.Fatalf("unexpected commands %+v", commands)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if code := commandCode(t, commands[0]); code != string(core.CodeBudgetExceeded) {
		t.Fatalf("code %q, want budget_exceeded", code)
	}
	if workRuns != 3 {
		t.Fatalf("completed work %d times, want 3 before budget_exceeded", workRuns)
	}
	if snapshot.Cost != 120 {
		t.Fatalf("cost %d, want 120", snapshot.Cost)
	}
}

func TestRunCostVisibleInEdgeCondition(t *testing.T) {
	def := &flow.IR{
		Name:    "cost-route",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"work": {Kind: "task"},
			"done": {Kind: "task"},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "work"},
			{From: "work", To: "done", When: "run.cost >= 50"},
		},
	}
	m := requireMachine(t, def)
	snapshot, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if commands[0].Node != "work" {
		t.Fatal(commands)
	}
	snapshot, commands, err = m.Apply(snapshot, flow.Event{
		Type:     "node_completed",
		Node:     "work",
		CostMpts: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Node != "done" {
		t.Fatalf("commands %+v, want done when run.cost >= 50", commands)
	}
	if snapshot.Cost != 50 {
		t.Fatalf("cost %d, want 50", snapshot.Cost)
	}
}

func startGate(t *testing.T) (*Machine, flow.Snapshot) {
	t.Helper()
	def := &flow.IR{
		Name:    "gate",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes:   map[string]flow.Node{"g": {Kind: "gate"}},
		Edges:   []flow.Edge{{From: ir.Start, To: "g"}},
	}
	m := requireMachine(t, def)
	snap, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Type != "await_gate" {
		t.Fatalf("commands %+v, want await_gate", commands)
	}
	return m, snap
}

func startWait(t *testing.T) (*Machine, flow.Snapshot) {
	t.Helper()
	def := &flow.IR{
		Name:    "wait",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes:   map[string]flow.Node{"w": {Kind: "wait"}},
		Edges:   []flow.Edge{{From: ir.Start, To: "w"}},
	}
	m := requireMachine(t, def)
	snap, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Type != "start_timer" {
		t.Fatalf("commands %+v, want start_timer", commands)
	}
	return m, snap
}
