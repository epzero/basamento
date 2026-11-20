package ir

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
)

func requireInvalid(t *testing.T, err error, fragments ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("want error")
	}
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %s; message %q", core.CodeOf(err), err)
	}
	for _, want := range fragments {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q does not contain %q", err.Error(), want)
		}
	}
}

func TestValidateAcceptsStartAndFailWithoutNodes(t *testing.T) {
	if err := Validate(simpleFlow()); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsReservedNodeNames(t *testing.T) {
	for _, name := range []string{Start, Fail} {
		f := simpleFlow()
		f.Nodes[name] = flow.Node{Kind: "task"}
		requireInvalid(t, Validate(f), `node "`+name+`"`, "reserved")
	}
}

func TestValidateRejectsControlCharactersInNodeIDs(t *testing.T) {
	for _, id := range []string{"plan\ncheck", "plan\tcheck", "plan\x00", "plan\x7f"} {
		f := simpleFlow()
		f.Nodes[id] = flow.Node{Kind: "task"}
		f.Edges = append(f.Edges, flow.Edge{From: "plan", To: id})
		requireInvalid(t, Validate(f), "node id", "control character")
	}
}

func TestValidateRejectsEdgeIntoStart(t *testing.T) {
	f := simpleFlow()
	f.Edges = append(f.Edges, flow.Edge{From: "verify", To: Start})
	requireInvalid(t, Validate(f), `edge "verify" -> "START"`, "only an edge source")
}

func TestValidateRejectsEdgeOutOfFail(t *testing.T) {
	f := simpleFlow()
	f.Edges = append(f.Edges, flow.Edge{From: Fail, To: "verify"})
	requireInvalid(t, Validate(f), `edge "FAIL" -> "verify"`, "only an edge target")
}

func TestValidateRequiresExactlyOneEdgeFromStart(t *testing.T) {
	for _, count := range []int{0, 2} {
		f := simpleFlow()
		f.Edges = f.Edges[1:] // drop START -> plan
		for i := 0; i < count; i++ {
			f.Edges = append([]flow.Edge{{From: Start, To: "plan"}}, f.Edges...)
		}
		requireInvalid(t, Validate(f), "exactly one edge from", `"START"`)
	}
}

func TestValidateRejectsUnknownEdgeEnds(t *testing.T) {
	f := simpleFlow()
	f.Edges[1].To = "missing"
	requireInvalid(t, Validate(f), `edge "plan" -> "missing"`, `"missing"`)

	f = simpleFlow()
	f.Edges[1].From = "missing"
	requireInvalid(t, Validate(f), `edge "missing" -> "verify"`, `"missing"`)
}

func TestValidateRejectsUnknownKind(t *testing.T) {
	f := simpleFlow()
	node := f.Nodes["plan"]
	node.Kind = "teleport"
	f.Nodes["plan"] = node
	requireInvalid(t, Validate(f), `node "plan"`, `unknown kind "teleport"`)
}

func TestValidateRejectsUnreachableNode(t *testing.T) {
	f := simpleFlow()
	f.Nodes["orphan"] = flow.Node{Kind: "task"}
	requireInvalid(t, Validate(f), `node "orphan"`, "unreachable")
}

func TestValidateRejectsUnguardedCycle(t *testing.T) {
	f := &flow.IR{
		Name:    "cycle",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes:   taskNodes("verify", "repair"),
		Edges: []flow.Edge{
			{From: Start, To: "verify"},
			{From: "verify", To: "repair"},
			{From: "repair", To: "verify"},
		},
	}
	requireInvalid(t, Validate(f), `node "repair"`, "guard")
}

func TestValidateAcceptsGuardedCycle(t *testing.T) {
	f := &flow.IR{
		Name:    "cycle",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"verify": {Kind: "task"},
			"repair": {Kind: "task", Guard: &flow.Guard{MaxVisits: 3}},
		},
		Edges: []flow.Edge{
			{From: Start, To: "verify"},
			{From: "verify", To: "repair"},
			{From: "repair", To: "verify"},
		},
	}
	if err := Validate(f); err != nil {
		t.Fatal(err)
	}
}

// A condition chooses when the cycle may leave; it can hold on every lap, so
// it is not a bound (REQ-FLOW-003, FLOW-003-02).
func TestValidateRejectsCycleGuardedOnlyByCondition(t *testing.T) {
	requireInvalid(t, Validate(guardedCycle(&flow.Guard{While: "true"})), `node "repair"`, "guard")
}

// FLOW-003-03: one visit budget is a bound.
func TestValidateAcceptsCycleBoundedByVisits(t *testing.T) {
	if err := Validate(guardedCycle(&flow.Guard{MaxVisits: 3})); err != nil {
		t.Fatal(err)
	}
}

// FLOW-003-04: so is one cost budget, with max_visits absent.
func TestValidateAcceptsCycleBoundedByCost(t *testing.T) {
	if err := Validate(guardedCycle(&flow.Guard{CostMpts: 5000})); err != nil {
		t.Fatal(err)
	}
}

// A condition alongside a budget is allowed: it narrows the exit, the budget
// does the bounding.
func TestValidateAcceptsCycleBoundedByVisitsAndCondition(t *testing.T) {
	if err := Validate(guardedCycle(&flow.Guard{MaxVisits: 3, While: "state.tests.failed > 0"})); err != nil {
		t.Fatal(err)
	}
}

// guardedCycle is the verify <-> repair cycle of compile.feature with guard on
// repair: the shape every guard test needs.
func guardedCycle(guard *flow.Guard) *flow.IR {
	return &flow.IR{
		Name:    "cycle",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"verify": {Kind: "task"},
			"repair": {Kind: "task", Guard: guard},
		},
		Edges: []flow.Edge{
			{From: Start, To: "verify"},
			{From: "verify", To: "repair"},
			{From: "repair", To: "verify"},
		},
	}
}

func TestValidateRejectsSelfLoopWithoutGuard(t *testing.T) {
	f := &flow.IR{
		Name:    "loop",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes:   taskNodes("retry"),
		Edges: []flow.Edge{
			{From: Start, To: "retry"},
			{From: "retry", To: "retry"},
		},
	}
	requireInvalid(t, Validate(f), `node "retry"`, "guard")
}

func TestValidateTreatsEmptyGuardAsNoGuard(t *testing.T) {
	f := &flow.IR{
		Name:    "cycle",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"verify": {Kind: "task"},
			"repair": {Kind: "task", Guard: &flow.Guard{}},
		},
		Edges: []flow.Edge{
			{From: Start, To: "verify"},
			{From: "verify", To: "repair"},
			{From: "repair", To: "verify"},
		},
	}
	requireInvalid(t, Validate(f), `node "repair"`, "guard")
}

func TestValidateReportsTheSameCycleEveryTime(t *testing.T) {
	names := []string{"repair", "verify", "plan", "check", "review"}
	var first string
	rnd := rand.New(rand.NewSource(1)) // seeded: the test reproduces the same orders
	for i := 0; i < 50; i++ {
		nodes := map[string]flow.Node{}
		for _, j := range rnd.Perm(len(names)) {
			nodes[names[j]] = flow.Node{Kind: "task"}
		}
		f := &flow.IR{
			Name:    "cycle",
			Version: 1,
			State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
			Nodes:   nodes,
			Edges: []flow.Edge{
				{From: Start, To: "plan"},
				{From: "plan", To: "verify"},
				{From: "verify", To: "repair"},
				{From: "repair", To: "verify"},
				{From: "plan", To: "check"},
				{From: "check", To: "review"},
			},
		}
		err := Validate(f)
		if err == nil {
			t.Fatal("want error")
		}
		if first == "" {
			first = err.Error()
		} else if err.Error() != first {
			t.Fatalf("iteration %d: %q != %q", i, err, first)
		}
	}
	if !strings.Contains(first, `node "repair"`) {
		t.Fatalf("names %q, want the lowest node of the cycle", first)
	}
}

func TestValidateRejectsInvalidSchema(t *testing.T) {
	f := simpleFlow()
	f.State.Schema = json.RawMessage(`{"type":"count"}`)
	requireInvalid(t, Validate(f), "state schema")
}

func TestValidateRejectsNilSchema(t *testing.T) {
	f := simpleFlow()
	f.State.Schema = nil
	requireInvalid(t, Validate(f), "state schema")
}

func TestValidateRejectsReducerOnUnknownKey(t *testing.T) {
	f := simpleFlow()
	f.State.Reducers = map[string]string{"notes": "append"}
	requireInvalid(t, Validate(f), `"notes"`, "reducer")
}

func TestValidateRejectsUnknownReducer(t *testing.T) {
	f := simpleFlow()
	f.State.Reducers = map[string]string{"tests": "shuffle"}
	requireInvalid(t, Validate(f), `"tests"`, "reducer")
}

func TestValidateAcceptsKnownReducers(t *testing.T) {
	f := simpleFlow()
	f.State.Reducers = map[string]string{"tests": "merge_by(path)"}
	if err := Validate(f); err != nil {
		t.Fatal(err)
	}
}

// The reducer check reads the state schema the way the compiler does: through
// local $ref and allOf, and a boolean true schema admits every object, so its
// keys cannot be missing from it.
func TestValidateResolvesReducerKeysThroughTheSchema(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		key    string
	}{
		{
			name:   "local $ref root",
			schema: `{"$defs":{"s":{"type":"object","properties":{"messages":{"type":"array"}}}},"$ref":"#/$defs/s"}`,
			key:    "messages",
		},
		{
			name:   "allOf composition",
			schema: `{"type":"object","properties":{"tests":{"type":"object"}},"allOf":[{"type":"object","properties":{"files":{"type":"array"}}}]}`,
			key:    "files",
		},
		{
			name:   "allOf through a $ref",
			schema: `{"$defs":{"s":{"type":"object","properties":{"attempts":{"type":"integer"}}}},"type":"object","allOf":[{"$ref":"#/$defs/s"}]}`,
			key:    "attempts",
		},
		{
			name:   "boolean true schema",
			schema: `true`,
			key:    "anything",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := simpleFlow()
			f.State.Schema = json.RawMessage(test.schema)
			f.State.Reducers = map[string]string{test.key: "append"}
			if err := Validate(f); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateStillRejectsAReducerKeyTheSchemaLacks(t *testing.T) {
	f := simpleFlow()
	f.State.Schema = json.RawMessage(`{"$defs":{"s":{"type":"object","properties":{"messages":{"type":"array"}}}},"$ref":"#/$defs/s"}`)
	f.State.Reducers = map[string]string{"notes": "append"}
	requireInvalid(t, Validate(f), `"notes"`, "reducer")
}

func TestValidateRequiresName(t *testing.T) {
	f := simpleFlow()
	f.Name = ""
	requireInvalid(t, Validate(f), "name")
}

// longChain is a flow of n distinct task nodes in a line from START to FAIL.
// Validation indexes edges once and walks cycles iteratively, so this shape
// used to be the worst case: the reachability walk rescanned every edge per
// node and the cycle search recursed once per node.
func longChain(n int) *flow.IR {
	f := &flow.IR{
		Name:    "chain",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes:   make(map[string]flow.Node, n),
		Edges:   make([]flow.Edge, 0, n+1),
	}
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("n%05d", i)
		f.Nodes[ids[i]] = flow.Node{Kind: "task"}
	}
	f.Edges = append(f.Edges, flow.Edge{From: Start, To: ids[0]})
	for i := 0; i+1 < len(ids); i++ {
		f.Edges = append(f.Edges, flow.Edge{From: ids[i], To: ids[i+1]})
	}
	f.Edges = append(f.Edges, flow.Edge{From: ids[len(ids)-1], To: Fail})
	return f
}

// TestValidateLongChain keeps the shape that used to exhaust the goroutine
// stack and the edge budget honest at the node maximum: it must validate with
// the accepted verdict. The cost is measured by BenchmarkValidateLongChain, not
// here, because a test may not read the wall clock.
func TestValidateLongChain(t *testing.T) {
	f := longChain(MaxNodes)
	if err := Validate(f); err != nil {
		t.Fatalf("validating a %d-node chain: %v", MaxNodes, err)
	}
	if got := len(f.Nodes); got != MaxNodes {
		t.Fatalf("built %d nodes, want %d", got, MaxNodes)
	}
}

func BenchmarkValidateLongChain(b *testing.B) {
	f := longChain(MaxNodes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := Validate(f); err != nil {
			b.Fatal(err)
		}
	}
}

// mapFlow is the smallest definition with a map node whose child is a declared
// task reached by an edge, so only a map defect can reject it.
func mapFlow() *flow.IR {
	return &flow.IR{
		Name:    "map",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
		Nodes: map[string]flow.Node{
			"fanout":      {Kind: "map", Spec: json.RawMessage(`{"over":"state.items","node":"fanout_item"}`)},
			"fanout_item": {Kind: "task"},
		},
		Edges: []flow.Edge{
			{From: Start, To: "fanout"},
			{From: "fanout", To: "fanout_item"},
			{From: "fanout_item", To: Fail},
		},
	}
}

func TestValidateAcceptsRunnableMapDefinition(t *testing.T) {
	if err := Validate(mapFlow()); err != nil {
		t.Fatal(err)
	}
}

// FLOW-062-01: a map child that is not a declared node.
func TestValidateRejectsMapChildThatIsNotDeclared(t *testing.T) {
	f := mapFlow()
	delete(f.Nodes, "fanout_item")
	f.Edges = []flow.Edge{
		{From: Start, To: "fanout"},
		{From: "fanout", To: Fail},
	}
	requireInvalid(t, Validate(f),
		`map node "fanout"`, `child node "fanout_item"`, "does not exist")
}

// FLOW-063-01: a map child of a kind the machine runs itself.
func TestValidateRejectsMapChildTheMachineRuns(t *testing.T) {
	for _, kind := range []string{"set", "parallel", "map"} {
		t.Run(kind, func(t *testing.T) {
			f := mapFlow()
			child := f.Nodes["fanout_item"]
			child.Kind = kind
			f.Nodes["fanout_item"] = child
			requireInvalid(t, Validate(f),
				`map node "fanout"`, `child node "fanout_item"`, `kind "`+kind+`"`,
				"which the machine runs itself")
		})
	}
}

// FLOW-064-01: one child claimed by two map nodes.
func TestValidateRejectsMapChildClaimedTwice(t *testing.T) {
	f := mapFlow()
	f.Nodes["review"] = flow.Node{
		Kind: "map",
		Spec: json.RawMessage(`{"over":"state.files","node":"fanout_item"}`),
	}
	f.Edges = append(f.Edges,
		flow.Edge{From: "fanout", To: "review"},
		flow.Edge{From: "review", To: Fail},
	)
	requireInvalid(t, Validate(f),
		`map nodes "fanout" and "review"`, `child node "fanout_item"`)
}

// FLOW-065-01: a map child that carries a guard.
func TestValidateRejectsGuardedMapChild(t *testing.T) {
	f := mapFlow()
	child := f.Nodes["fanout_item"]
	child.Guard = &flow.Guard{MaxVisits: 3}
	f.Nodes["fanout_item"] = child
	requireInvalid(t, Validate(f),
		`map node "fanout"`, `child node "fanout_item"`, "a guard counts visits",
		"a map child runs once per element")
}

// The map-child guard is reported before the cycle search, so a guarded map
// cycle names the child guard rather than looking bounded (design.md §2.6).
func TestValidateReportsGuardedMapChildBeforeCycle(t *testing.T) {
	f := mapFlow()
	child := f.Nodes["fanout_item"]
	child.Guard = &flow.Guard{MaxVisits: 3}
	f.Nodes["fanout_item"] = child
	f.Edges = []flow.Edge{
		{From: Start, To: "fanout"},
		{From: "fanout", To: "fanout_item"},
		{From: "fanout_item", To: "fanout"},
	}
	requireInvalid(t, Validate(f),
		`map node "fanout"`, `child node "fanout_item"`, "has a guard")
}

// FLOW-066-01: no condition on the edge from START.
func TestValidateRejectsConditionOnStartEdge(t *testing.T) {
	f := simpleFlow()
	f.Edges[0].When = "true"
	requireInvalid(t, Validate(f), `edge "START" -> "plan"`, "takes no condition")
}

func TestValidateAcceptsConditionOnLaterEdge(t *testing.T) {
	f := simpleFlow()
	f.Edges[1].When = "true"
	if err := Validate(f); err != nil {
		t.Fatal(err)
	}
}
