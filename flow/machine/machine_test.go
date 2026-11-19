package machine

import (
	"encoding/json"
	"math/rand"
	"strconv"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

func triageFlow() *flow.IR {
	return &flow.IR{
		Name:    "triage",
		Version: 1,
		State: flow.StateSpec{
			Schema:   json.RawMessage(`{"type":"object","properties":{"failed":{"type":"integer"}}}`),
			Reducers: map[string]string{"failed": "replace"},
		},
		Nodes: map[string]flow.Node{
			"verify": {Kind: "task", Out: "failed"},
			"review": {Kind: "task"},
			"repair": {Kind: "task"},
			"fail":   {Kind: "task"},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "verify"},
			{From: "verify", To: "review", When: "state.failed == 0"},
			{From: "verify", To: "repair", When: "state.failed > 0"},
			{From: "verify", To: "fail"},
		},
	}
}

func requireMachine(t *testing.T, def *flow.IR) *Machine {
	t.Helper()
	m, err := New(def)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return m
}

func requireCommands(t *testing.T, commands []flow.Command) {
	t.Helper()
	if len(commands) == 0 {
		t.Fatal("want commands, got none")
	}
}

func TestRouteFollowsFirstEdgeWhoseConditionHolds(t *testing.T) {
	for _, tc := range []struct {
		failed int
		want   string
	}{
		{failed: 0, want: "review"},
		{failed: 2, want: "repair"},
		{failed: -1, want: "fail"},
	} {
		t.Run(strconv.Itoa(tc.failed), func(t *testing.T) {
			m := requireMachine(t, triageFlow())
			snapshot, commands, err := m.Start(json.RawMessage(`{}`))
			if err != nil {
				t.Fatal(err)
			}
			requireCommands(t, commands)
			if commands[0].Type != "run_node" || commands[0].Node != "verify" {
				t.Fatalf("first command %+v, want run_node verify", commands[0])
			}
			snapshot, commands, err = m.Apply(snapshot, flow.Event{
				Type:    "node_completed",
				Node:    "verify",
				Payload: json.RawMessage(strconv.Itoa(tc.failed)),
			})
			if err != nil {
				t.Fatal(err)
			}
			requireCommands(t, commands)
			if commands[0].Node != tc.want {
				t.Fatalf("next node %q, want %q", commands[0].Node, tc.want)
			}
			assertState(t, snapshot, `{"failed":`+strconv.Itoa(tc.failed)+`}`)
		})
	}
}

func TestFirstMatchingConditionWins(t *testing.T) {
	def := triageFlow()
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "verify"},
		{From: "verify", To: "review", When: "state.failed < 1"},
		{From: "verify", To: "repair", When: "state.failed == 0"},
		{From: "verify", To: "fail"},
	}
	m := requireMachine(t, def)
	snapshot, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_, commands, err := m.Apply(snapshot, flow.Event{
		Type:    "node_completed",
		Node:    "verify",
		Payload: json.RawMessage(`0`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if commands[0].Node != "review" {
		t.Fatalf("next node %q, want review (declared first)", commands[0].Node)
	}
}

func TestAnUnconditionalEdgeIsOnlyTheFallback(t *testing.T) {
	def := triageFlow()
	delete(def.Nodes, "repair")
	def.Edges = []flow.Edge{
		{From: ir.Start, To: "verify"},
		{From: "verify", To: "fail"},
		{From: "verify", To: "review", When: "state.failed == 0"},
	}
	m := requireMachine(t, def)
	snapshot, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_, commands, err := m.Apply(snapshot, flow.Event{
		Type:    "node_completed",
		Node:    "verify",
		Payload: json.RawMessage(`0`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if commands[0].Node != "review" {
		t.Fatalf("next node %q, want review (the condition holds)", commands[0].Node)
	}
}

func TestStartUsesTheEntryEdge(t *testing.T) {
	def := triageFlow()
	def.Edges = append([]flow.Edge{{From: ir.Start, To: "verify"}}, def.Edges[1:]...)
	m := requireMachine(t, def)
	_, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Node != "verify" {
		t.Fatalf("commands %+v, want one for verify", commands)
	}
}

func TestFailEndpointFailsTheRun(t *testing.T) {
	def := &flow.IR{
		Name:    "ending",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes:   map[string]flow.Node{"a": {Kind: "task"}},
		Edges:   []flow.Edge{{From: ir.Start, To: "a"}, {From: "a", To: ir.Fail}},
	}
	m := requireMachine(t, def)
	snapshot, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if commands[0].Node != "a" {
		t.Fatalf("commands %+v, want a", commands)
	}
	_, commands, err = m.Apply(snapshot, flow.Event{Type: "node_completed", Node: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Type != "fail" {
		t.Fatalf("commands %+v, want fail", commands)
	}
}

func TestDeadEndCompletesTheRun(t *testing.T) {
	def := &flow.IR{
		Name:    "ending",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes:   map[string]flow.Node{"a": {Kind: "task"}},
		Edges:   []flow.Edge{{From: ir.Start, To: "a"}},
	}
	m := requireMachine(t, def)
	snapshot, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_, commands, err := m.Apply(snapshot, flow.Event{Type: "node_completed", Node: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Type != "complete" {
		t.Fatalf("commands %+v, want complete", commands)
	}
}

func TestNodeFailureFailsTheRun(t *testing.T) {
	m := requireMachine(t, triageFlow())
	snapshot, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"code":"vendor_error"}`)
	_, commands, err := m.Apply(snapshot, flow.Event{Type: "node_failed", Node: "verify", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Type != "fail" || string(commands[0].Input) != string(payload) {
		t.Fatalf("commands %+v, want fail carrying %s", commands, payload)
	}
}

func TestSetNodesRunInTheMachine(t *testing.T) {
	def := &flow.IR{
		Name:    "pure",
		Version: 1,
		State: flow.StateSpec{
			Schema:   json.RawMessage(`{"type":"object","properties":{"greeting":{"type":"string"},"n":{"type":"integer"}}}`),
			Reducers: map[string]string{"n": "sum"},
		},
		Nodes: map[string]flow.Node{
			"prep": {Kind: "set", Spec: json.RawMessage(`{"greeting":"hi","n":1}`)},
			"more": {Kind: "set", Spec: json.RawMessage(`{"n":2}`)},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "prep"}, {From: "prep", To: "more"}},
	}
	m := requireMachine(t, def)
	snapshot, commands, err := m.Start(json.RawMessage(`{"n":40}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Type != "complete" {
		t.Fatalf("commands %+v, want complete", commands)
	}
	assertState(t, snapshot, `{"greeting":"hi","n":43}`)
}

func TestSetExpressionUsesCEL(t *testing.T) {
	def := &flow.IR{
		Name:    "pure",
		Version: 1,
		State: flow.StateSpec{
			Schema:   json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}}}`),
			Reducers: map[string]string{"n": "sum"},
		},
		Nodes: map[string]flow.Node{
			"prep": {Kind: "set", Out: "n", Spec: json.RawMessage(`"state.n * 2"`)},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "prep"}},
	}
	m := requireMachine(t, def)
	snapshot, _, err := m.Start(json.RawMessage(`{"n":21}`))
	if err != nil {
		t.Fatal(err)
	}
	assertState(t, snapshot, `{"n":63}`)
}

func TestGuardVisitLimitFailsWithBudgetExceeded(t *testing.T) {
	def := &flow.IR{
		Name:    "loop",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"loop": {Kind: "task", Guard: &flow.Guard{MaxVisits: 2}},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "loop"}, {From: "loop", To: "loop"}},
	}
	m := requireMachine(t, def)
	snapshot, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if len(commands) != 1 || commands[0].Node != "loop" {
			t.Fatalf("round %d: commands %+v, want loop", i, commands)
		}
		snapshot, commands, err = m.Apply(snapshot, flow.Event{Type: "node_completed", Node: "loop"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(commands) != 1 || commands[0].Type != "fail" {
		t.Fatalf("commands %+v, want fail", commands)
	}
	if code := commandCode(t, commands[0]); code != string(core.CodeBudgetExceeded) {
		t.Fatalf("code %q, want budget_exceeded", code)
	}
}

func TestGuardWhileFalseSkipsTheNode(t *testing.T) {
	def := &flow.IR{
		Name:    "guarded",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object","properties":{"go":{"type":"boolean"}}}`)},
		Nodes: map[string]flow.Node{
			"work": {Kind: "task", Guard: &flow.Guard{While: "state.go"}},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "work"}},
	}
	m := requireMachine(t, def)
	snapshot, commands, err := m.Start(json.RawMessage(`{"go":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 1 || commands[0].Type != "complete" {
		t.Fatalf("commands %+v, want complete", commands)
	}
	if snapshot.Visit["work"] != 0 {
		t.Fatalf("visit count %d, want 0", snapshot.Visit["work"])
	}
}

func TestParallelStartsEveryChildInOneSuperstep(t *testing.T) {
	def := fanoutFlow(3)
	m := requireMachine(t, def)
	_, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 3 {
		t.Fatalf("commands %+v, want three", commands)
	}
	for i, want := range []string{"a", "b", "c"} {
		if commands[i].Node != want || commands[i].Type != "run_node" {
			t.Fatalf("command %d = %+v, want run_node %s", i, commands[i], want)
		}
	}
}

func TestCompletionOrderDoesNotChangeTheSnapshot(t *testing.T) {
	orders := permutations([]string{"a", "b", "c"})
	var first flow.Snapshot
	for i, order := range orders {
		snapshot := runFanout(t, 3, order)
		if i == 0 {
			first = snapshot
			continue
		}
		if string(snapshot.State) != string(first.State) {
			t.Fatalf("order %v state %s, want %s", order, snapshot.State, first.State)
		}
		if !snapshotsEqual(snapshot, first) {
			t.Fatalf("order %v snapshot differs", order)
		}
	}
	assertState(t, first, `{"log":["a","b","c"]}`)
}

// TestPropertyCompletionOrderIndependence checks random fan-outs and random
// completion orders: the merged state is always the canonical node-id order and
// total cost_mpts is independent of completion order.
func TestPropertyCompletionOrderIndependence(t *testing.T) {
	for n := 2; n <= 6; n++ {
		want := make([]string, n)
		var wantCost int64
		for i := range want {
			want[i] = nodeName(i)
			wantCost += int64((i + 1) * 10)
		}
		rng := rand.New(rand.NewSource(int64(n)))
		for round := 0; round < 50; round++ {
			order := rng.Perm(n)
			names := make([]string, n)
			for i, at := range order {
				names[i] = nodeName(at)
			}
			snapshot := runFanout(t, n, names)
			var state struct {
				Log []string `json:"log"`
			}
			if err := json.Unmarshal(snapshot.State, &state); err != nil {
				t.Fatal(err)
			}
			if strings.Join(state.Log, ",") != strings.Join(want, ",") {
				t.Fatalf("n=%d order %v: log %v, want %v", n, names, state.Log, want)
			}
			if snapshot.Cost != wantCost {
				t.Fatalf("n=%d order %v: cost %d, want %d", n, names, snapshot.Cost, wantCost)
			}
		}
	}
}

func TestSnapshotsEqualAcrossCompletionOrders(t *testing.T) {
	first := runFanout(t, 4, []string{"d", "b", "a", "c"})
	second := runFanout(t, 4, []string{"a", "b", "c", "d"})
	if !snapshotsEqual(first, second) {
		t.Fatalf("snapshots differ:\n%s\n%s", first.State, second.State)
	}
}

func TestInputMappingBuildsTheCommandInput(t *testing.T) {
	def := &flow.IR{
		Name:    "mapping",
		Version: 1,
		State: flow.StateSpec{
			Schema:   json.RawMessage(`{"type":"object","properties":{"draft":{"type":"object"},"worker":{"type":"object"}}}`),
			Reducers: map[string]string{"worker": "replace"},
		},
		Nodes: map[string]flow.Node{
			"worker": {Kind: "task", In: "state.draft", Out: "worker"},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "worker"}},
	}
	m := requireMachine(t, def)
	_, commands, err := m.Start(json.RawMessage(`{"draft":{"text":"hello"},"extra":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(commands[0].Input) != `{"text":"hello"}` {
		t.Fatalf("input %s, want the draft", commands[0].Input)
	}
}

func TestDefaultsFillMissingInputKeys(t *testing.T) {
	def := &flow.IR{
		Name:     "defaults",
		Version:  1,
		State:    flow.StateSpec{Schema: json.RawMessage(`{"type":"object","properties":{"model":{"type":"string"}}}`)},
		Nodes:    map[string]flow.Node{"a": {Kind: "task"}},
		Edges:    []flow.Edge{{From: ir.Start, To: "a"}},
		Defaults: map[string]json.RawMessage{"model": json.RawMessage(`"haiku"`)},
	}
	m := requireMachine(t, def)
	snapshot, _, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	assertState(t, snapshot, `{"model":"haiku"}`)
}

func TestMapFansOutOverTheState(t *testing.T) {
	def := &flow.IR{
		Name:    "map",
		Version: 1,
		State: flow.StateSpec{
			Schema:   json.RawMessage(`{"type":"object","properties":{"items":{"type":"array"},"out":{"type":"array"}}}`),
			Reducers: map[string]string{"out": "append"},
		},
		Nodes: map[string]flow.Node{
			"fan":  {Kind: "map", Spec: json.RawMessage(`{"over":"state.items","node":"work"}`)},
			"work": {Kind: "task", Out: "out"},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "fan"},
			{From: "fan", To: "work"},
		},
	}
	m := requireMachine(t, def)
	snapshot, commands, err := m.Start(json.RawMessage(`{"items":[1,2,3]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != 3 {
		t.Fatalf("commands %+v, want three", commands)
	}
	for i, want := range []string{"1", "2", "3"} {
		if string(commands[i].Input) != want || commands[i].Attempt != i+1 {
			t.Fatalf("command %d = %+v, want input %s and attempt %d", i, commands[i], want, i+1)
		}
	}
	for i := range commands {
		event := flow.Event{Type: "node_completed", Node: "work", Attempt: commands[i].Attempt}
		event.Payload = json.RawMessage(strconv.Itoa(i + 1))
		snapshot, _, err = m.Apply(snapshot, event)
		if err != nil {
			t.Fatal(err)
		}
	}
	assertState(t, snapshot, `{"items":[1,2,3],"out":[1,2,3]}`)
}

func TestRejectsUncompilableExpression(t *testing.T) {
	def := triageFlow()
	def.Edges[1].When = "state.missing == 0"
	if _, err := New(def); err == nil {
		t.Fatal("want an error")
	} else if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %s, want invalid_argument", core.CodeOf(err))
	}
}

func fanoutFlow(n int) *flow.IR {
	nodes := map[string]flow.Node{
		"fanout": {Kind: "parallel"},
	}
	edges := []flow.Edge{{From: ir.Start, To: "fanout"}}
	for i := 0; i < n; i++ {
		id := nodeName(i)
		nodes[id] = flow.Node{Kind: "task", Out: "log"}
		edges = append(edges, flow.Edge{From: "fanout", To: id})
	}
	return &flow.IR{
		Name:    "fanout",
		Version: 1,
		State: flow.StateSpec{
			Schema:   json.RawMessage(`{"type":"object","properties":{"log":{"type":"array"}}}`),
			Reducers: map[string]string{"log": "append"},
		},
		Nodes: nodes,
		Edges: edges,
	}
}

// nodeName gives the i-th fan-out child a lexicographically ordered id, so
// node-id order and declaration order differ and the tests can tell them apart.
func nodeName(i int) string { return string(rune('a' + i)) }

func runFanout(t *testing.T, n int, order []string) flow.Snapshot {
	t.Helper()
	m := requireMachine(t, fanoutFlow(n))
	snapshot, commands, err := m.Start(json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(commands) != n {
		t.Fatalf("commands %+v, want %d", commands, n)
	}
	for _, name := range order {
		payload, _ := json.Marshal(name)
		cost := int64(name[0]-'a'+1) * 10
		snapshot, commands, err = m.Apply(snapshot, flow.Event{
			Type:     "node_completed",
			Node:     name,
			Payload:  payload,
			CostMpts: cost,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(commands) != 1 || commands[0].Type != "complete" {
		t.Fatalf("commands %+v, want complete", commands)
	}
	return snapshot
}

func assertState(t *testing.T, snapshot flow.Snapshot, want string) {
	t.Helper()
	if string(snapshot.State) != want {
		t.Fatalf("state %s, want %s", snapshot.State, want)
	}
}

func commandCode(t *testing.T, command flow.Command) string {
	t.Helper()
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(command.Input, &payload); err != nil {
		t.Fatalf("command input %s: %v", command.Input, err)
	}
	return payload.Code
}

func snapshotsEqual(a, b flow.Snapshot) bool {
	left, err := json.Marshal(a)
	if err != nil {
		return false
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(left) == string(right)
}

func TestNodeInputWithoutInUsesRunInput(t *testing.T) {
	def := &flow.IR{
		Name:    "input-default",
		Version: 1,
		State: flow.StateSpec{
			Schema: json.RawMessage(`{"type":"object"}`),
		},
		Nodes: map[string]flow.Node{
			"work": {Kind: "task"},
		},
		Edges: []flow.Edge{{From: ir.Start, To: "work"}},
	}
	for _, tc := range []struct {
		name  string
		start string
		want  string
	}{
		{name: "run_input", start: `{"tests":4}`, want: `{"tests":4}`},
		{name: "empty_run", start: `{}`, want: `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := requireMachine(t, def)
			_, commands, err := m.Start(json.RawMessage(tc.start))
			if err != nil {
				t.Fatal(err)
			}
			requireCommands(t, commands)
			if string(commands[0].Input) != tc.want {
				t.Fatalf("input %s, want %s", commands[0].Input, tc.want)
			}
		})
	}
	t.Run("in_mapping", func(t *testing.T) {
		mapped := &flow.IR{
			Name:    "input-mapped",
			Version: 1,
			State: flow.StateSpec{
				Schema: json.RawMessage(`{"type":"object","properties":{"draft":{"type":"object"}}}`),
			},
			Nodes: map[string]flow.Node{
				"work": {Kind: "task", In: "state.draft"},
			},
			Edges: []flow.Edge{{From: ir.Start, To: "work"}},
		}
		m := requireMachine(t, mapped)
		_, commands, err := m.Start(json.RawMessage(`{"draft":{"picked":true},"tests":4}`))
		if err != nil {
			t.Fatal(err)
		}
		requireCommands(t, commands)
		if string(commands[0].Input) != `{"picked":true}` {
			t.Fatalf("input %s, want mapped draft", commands[0].Input)
		}
	})
}

func permutations(items []string) [][]string {
	var out [][]string
	var walk func(int)
	walk = func(at int) {
		if at == len(items) {
			out = append(out, append([]string(nil), items...))
			return
		}
		for i := at; i < len(items); i++ {
			items[at], items[i] = items[i], items[at]
			walk(at + 1)
			items[at], items[i] = items[i], items[at]
		}
	}
	walk(0)
	return out
}
