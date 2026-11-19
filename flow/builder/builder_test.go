package builder

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/core/schema"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/compile"
	"github.com/epzero/basamento/flow/ir"
)

func mustBuild(t *testing.T, b *Builder) *flow.IR {
	t.Helper()
	built, err := b.Build()
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return built
}

func mustCanonical(t *testing.T, f *flow.IR) []byte {
	t.Helper()
	raw, err := ir.Canonical(f)
	if err != nil {
		t.Fatalf("canonical IR: %v", err)
	}
	return raw
}

func mustFailure(t *testing.T, b *Builder) *core.Error {
	t.Helper()
	built, err := b.Build()
	if err == nil {
		t.Fatalf("Build succeeded, want an error: %+v", built)
	}
	var platform *core.Error
	if !errors.As(err, &platform) {
		t.Fatalf("error is %T, want *core.Error", err)
	}
	if platform.Code != core.CodeInvalidArgument {
		t.Fatalf("code %s, want %s: %s", platform.Code, core.CodeInvalidArgument, platform.Message)
	}
	return platform
}

func TestBuildMatchesEquivalentDataFlow(t *testing.T) {
	built := mustBuild(t, New("codegen", 3).
		Node("plan", KindAgent, json.RawMessage(`{"agent": "planner"}`)).
		Node("verify", KindTool, json.RawMessage(`{"tool": "run_tests"}`)).
		Edge(Start, "plan", "").
		Edge("plan", "verify", "").
		Edge("verify", Fail, ""))

	data := &flow.IR{
		Name:    "codegen",
		Version: 3,
		State:   flow.StateSpec{Schema: defaultStateSchema},
		Nodes: map[string]flow.Node{
			"plan":   {Kind: "agent", Spec: json.RawMessage(`{"agent":"planner"}`)},
			"verify": {Kind: "tool", Spec: json.RawMessage(`{"tool":"run_tests"}`)},
		},
		Edges: []flow.Edge{
			{From: Start, To: "plan"},
			{From: "plan", To: "verify"},
			{From: "verify", To: Fail},
		},
	}

	if got, want := string(mustCanonical(t, built)), string(mustCanonical(t, data)); got != want {
		t.Fatalf("built IR differs from data flow:\n got %s\nwant %s", got, want)
	}
	if got, want := ir.HashCanonical(mustCanonical(t, built)), ir.HashCanonical(mustCanonical(t, data)); got != want {
		t.Fatalf("content hash %s, want %s", got, want)
	}
}

func TestEveryNodeKindAndAttribute(t *testing.T) {
	state := json.RawMessage(`{"type":"object","properties":{"plan":{},"approved":{}}}`)
	guard := flow.Guard{MaxVisits: 3, CostMpts: 5000, While: "state.go"}
	b := New("kinds", 1).StateSchema(state)
	kinds := []struct {
		name string
		kind Kind
	}{
		{"s", KindSet}, {"t", KindTask}, {"m", KindModel}, {"a", KindAgent}, {"k", KindTool},
		{"f", KindMap}, {"p", KindParallel}, {"sub", KindSubflow}, {"g", KindGate}, {"w", KindWait},
	}
	for _, entry := range kinds {
		opts := []NodeOption{}
		spec := json.RawMessage(nil)
		if entry.kind == KindGate {
			spec = json.RawMessage(`{"schema": "review.json"}`)
			opts = []NodeOption{In("state.plan"), Out("approved"), Guard(guard)}
		}
		b.Node(entry.name, entry.kind, spec, opts...)
	}
	b.Edge(Start, "s", "")

	built := mustBuild(t, b)
	if len(built.Nodes) != 10 {
		t.Fatalf("got %d nodes, want 10", len(built.Nodes))
	}
	for _, entry := range kinds {
		if got := built.Nodes[entry.name].Kind; got != string(entry.kind) {
			t.Fatalf("node %q kind %q, want %q", entry.name, got, entry.kind)
		}
	}
	gate := built.Nodes["g"]
	if string(gate.Spec) != `{"schema":"review.json"}` {
		t.Fatalf("gate spec %s", gate.Spec)
	}
	if gate.In != "state.plan" || gate.Out != "approved" {
		t.Fatalf("gate in/out %q/%q", gate.In, gate.Out)
	}
	if gate.Guard == nil || *gate.Guard != guard {
		t.Fatalf("gate guard %+v, want %+v", gate.Guard, guard)
	}
}

func TestEdgesKeepDeclarationOrderAndConditions(t *testing.T) {
	built := mustBuild(t, New("edges", 1).
		Node("plan", KindAgent, nil).
		Node("verify", KindTool, nil).
		Node("repair", KindAgent, nil).
		Node("review", KindGate, nil).
		Edge(Start, "plan", "").
		Edge("plan", "verify", "").
		Edge("verify", "review", "state.failed == 0").
		Edge("verify", "repair", "state.failed > 0").
		Edge("verify", Fail, ""))

	want := []flow.Edge{
		{From: Start, To: "plan"},
		{From: "plan", To: "verify"},
		{From: "verify", To: "review", When: "state.failed == 0"},
		{From: "verify", To: "repair", When: "state.failed > 0"},
		{From: "verify", To: Fail},
	}
	if !reflect.DeepEqual(built.Edges, want) {
		t.Fatalf("edges %+v, want %+v", built.Edges, want)
	}
}

func TestEntryAddsStartEdge(t *testing.T) {
	built := mustBuild(t, New("entry", 1).
		Node("plan", KindAgent, nil).
		Edge("plan", Fail, "").
		Entry("plan"))

	want := []flow.Edge{
		{From: "plan", To: Fail},
		{From: Start, To: "plan"},
	}
	if !reflect.DeepEqual(built.Edges, want) {
		t.Fatalf("edges %+v, want %+v", built.Edges, want)
	}
}

func TestStartAndFailNeedNoNodes(t *testing.T) {
	built := mustBuild(t, New("ends", 1).
		Node("plan", KindAgent, nil).
		Edge(Start, "plan", "").
		Edge("plan", Fail, ""))
	if len(built.Nodes) != 1 {
		t.Fatalf("nodes %+v, want only plan", built.Nodes)
	}
}

func TestDuplicateNodeRejected(t *testing.T) {
	platform := mustFailure(t, New("codegen", 3).
		Node("plan", KindAgent, nil).
		Node("plan", KindTool, nil).
		Edge(Start, "plan", ""))
	if want := `flow "codegen": duplicate node "plan"`; platform.Message != want {
		t.Fatalf("message %q, want %q", platform.Message, want)
	}
	if got := platform.Details["node"]; got != "plan" {
		t.Fatalf("details node %v", got)
	}
}

func TestUnknownEdgeEndpointRejected(t *testing.T) {
	tests := []struct {
		name    string
		from    string
		to      string
		missing string
	}{
		{name: "unknown target", from: "plan", to: "verify", missing: "verify"},
		{name: "unknown source", from: "build", to: "plan", missing: "build"},
		{name: "fail is only a target", from: "FAIL", to: "plan", missing: "FAIL"},
		{name: "start is only a source", from: "plan", to: "START", missing: "START"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			platform := mustFailure(t, New("codegen", 3).
				Node("plan", KindAgent, nil).
				Edge(Start, "plan", "").
				Edge(test.from, test.to, ""))
			if want := `unknown node "` + test.missing + `"`; !strings.Contains(platform.Message, want) {
				t.Fatalf("message %q does not contain %q", platform.Message, want)
			}
			if got := platform.Details["node"]; got != test.missing {
				t.Fatalf("details node %v, want %s", got, test.missing)
			}
		})
	}
}

func TestNoEntryRejected(t *testing.T) {
	platform := mustFailure(t, New("codegen", 3).
		Node("plan", KindAgent, nil).
		Node("verify", KindTool, nil).
		Edge("plan", "verify", ""))
	if !strings.Contains(platform.Message, "no entry") {
		t.Fatalf("message %q does not state there is no entry", platform.Message)
	}
}

func TestTwoEntryNodesRejected(t *testing.T) {
	platform := mustFailure(t, New("codegen", 3).
		Node("plan", KindAgent, nil).
		Node("verify", KindTool, nil).
		Entry("plan").
		Entry("verify"))
	for _, name := range []string{`node "plan"`, `node "verify"`} {
		if !strings.Contains(platform.Message, name) {
			t.Fatalf("message %q does not name %q", platform.Message, name)
		}
	}
	names, ok := platform.Details["nodes"].([]string)
	if !ok || !reflect.DeepEqual(names, []string{"plan", "verify"}) {
		t.Fatalf("details nodes %v", platform.Details["nodes"])
	}
}

func TestSameEntryNodeTwiceIsNotTwoNodes(t *testing.T) {
	built := mustBuild(t, New("codegen", 3).
		Node("plan", KindAgent, nil).
		Edge("plan", Fail, "").
		Entry("plan").
		Entry("plan"))
	if got := countEdges(built, Start, "plan"); got != 1 {
		t.Fatalf("%d entry edges, want 1", got)
	}
	if _, err := compile.Compile(built); err != nil {
		t.Fatalf("compile built IR: %v", err)
	}
}

func TestStartEdges(t *testing.T) {
	state := json.RawMessage(`{"type":"object","properties":{"plan":{}}}`)
	base := func() *Builder {
		return New("codegen", 3).StateSchema(state)
	}
	addPlan := func(b *Builder) *Builder {
		return b.Node("plan", KindAgent, nil).Edge("plan", Fail, "")
	}

	tests := []struct {
		name    string
		build   func() *Builder
		wantErr bool
		errIn   string
	}{
		{
			name: "two different entry marks",
			build: func() *Builder {
				return addPlan(base().Node("verify", KindTool, nil)).
					Entry("plan").
					Entry("verify")
			},
			wantErr: true,
			errIn:   `node "plan"`,
		},
		{
			name: "two raw start edges different nodes",
			build: func() *Builder {
				return addPlan(base().Node("verify", KindTool, nil).Edge("verify", Fail, "")).
					Edge(Start, "plan", "").
					Edge(Start, "verify", "")
			},
			wantErr: true,
			errIn:   `node "plan"`,
		},
		{
			name: "same raw start edge twice",
			build: func() *Builder {
				return addPlan(base()).
					Edge(Start, "plan", "").
					Edge(Start, "plan", "")
			},
			wantErr: true,
			errIn:   "more than one edge",
		},
		{
			name: "raw start and entry on same node",
			build: func() *Builder {
				return addPlan(base()).
					Edge(Start, "plan", "").
					Entry("plan")
			},
			wantErr: false,
		},
		{
			name: "raw start and entry on different nodes",
			build: func() *Builder {
				return addPlan(base().Node("verify", KindTool, nil).Edge("verify", Fail, "")).
					Edge(Start, "plan", "").
					Entry("verify")
			},
			wantErr: true,
			errIn:   `node "plan"`,
		},
		{
			name: "no start edge at all",
			build: func() *Builder {
				return base().Node("plan", KindAgent, nil).Edge("plan", Fail, "")
			},
			wantErr: true,
			errIn:   "no entry",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			b := test.build()
			built, err := b.Build()
			if test.wantErr {
				if err == nil {
					t.Fatal("Build succeeded, want error")
				}
				if test.errIn != "" && !strings.Contains(err.Error(), test.errIn) {
					t.Fatalf("error %q does not contain %q", err.Error(), test.errIn)
				}
				return
			}
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			startCount := 0
			for _, edge := range built.Edges {
				if edge.From == Start {
					startCount++
				}
			}
			if startCount != 1 {
				t.Fatalf("%d edges from START, want 1", startCount)
			}
			if _, err := compile.Compile(built); err != nil {
				t.Fatalf("compile built IR: %v", err)
			}
		})
	}
}

func TestOutputOutsideStateRejected(t *testing.T) {
	platform := mustFailure(t, New("codegen", 3).
		StateSchema(json.RawMessage(`{"type":"object","properties":{"plan":{},"tests":{}}}`)).
		Node("plan", KindAgent, nil, Out("result")).
		Edge(Start, "plan", ""))
	for _, want := range []string{`node "plan"`, `"result"`} {
		if !strings.Contains(platform.Message, want) {
			t.Fatalf("message %q does not contain %q", platform.Message, want)
		}
	}
}

func TestStateSchemaAndReducersWritten(t *testing.T) {
	built := mustBuild(t, New("reducers", 1).
		StateSchema(json.RawMessage(`{"type":"object","properties":{"messages":{},"attempts":{}}}`)).
		Reducer("messages", "append").
		Reducer("attempts", "sum").
		Node("plan", KindAgent, nil).
		Edge(Start, "plan", ""))

	if got := string(built.State.Schema); got != `{"properties":{"attempts":{},"messages":{}},"type":"object"}` {
		t.Fatalf("schema %s", got)
	}
	want := map[string]string{"messages": "append", "attempts": "sum"}
	if !reflect.DeepEqual(built.State.Reducers, want) {
		t.Fatalf("reducers %+v, want %+v", built.State.Reducers, want)
	}
}

func TestReducerOnUnknownKeyRejected(t *testing.T) {
	platform := mustFailure(t, New("codegen", 3).
		StateSchema(json.RawMessage(`{"type":"object","properties":{"tests":{"type":"integer"}}}`)).
		Reducer("notes", "append"))
	if want := `state key "notes"`; !strings.Contains(platform.Message, want) {
		t.Fatalf("message %q does not contain %q", platform.Message, want)
	}
}

func TestDuplicateReducerRejectedWhateverTheOrder(t *testing.T) {
	state := json.RawMessage(`{"type":"object","properties":{"a":{}}}`)
	orders := [][2][2]string{
		{{"a", "append"}, {"a", "sum"}},
		{{"a", "sum"}, {"a", "append"}},
	}
	for _, order := range orders {
		t.Run(order[0][1]+" then "+order[1][1], func(t *testing.T) {
			platform := mustFailure(t, New("codegen", 3).
				StateSchema(state).
				Reducer(order[0][0], order[0][1]).
				Reducer(order[1][0], order[1][1]).
				Node("plan", KindAgent, nil).
				Edge(Start, "plan", ""))
			if want := `flow "codegen": duplicate reducer for state key "a"`; platform.Message != want {
				t.Fatalf("message %q, want %q", platform.Message, want)
			}
			if got := platform.Details["reducer"]; got != "a" {
				t.Fatalf("details reducer %v", got)
			}
		})
	}
}

func TestNilStateTypeRejected(t *testing.T) {
	platform := mustFailure(t, New("codegen", 3).
		StateType(nil).
		Node("plan", KindAgent, nil).
		Edge(Start, "plan", ""))
	if want := `flow "codegen": state type is nil`; platform.Message != want {
		t.Fatalf("message %q, want %q", platform.Message, want)
	}
}

func TestExplicitlyEmptyOutputRejected(t *testing.T) {
	platform := mustFailure(t, New("codegen", 3).
		StateSchema(json.RawMessage(`{"type":"object","properties":{"plan":{}}}`)).
		Node("plan", KindAgent, nil, Out("")).
		Edge(Start, "plan", ""))
	if want := `flow "codegen": node "plan": output key is empty`; platform.Message != want {
		t.Fatalf("message %q, want %q", platform.Message, want)
	}
}

func TestExplicitlyEmptyInputRejected(t *testing.T) {
	platform := mustFailure(t, New("codegen", 3).
		Node("plan", KindAgent, nil, In("")).
		Edge(Start, "plan", ""))
	if want := `flow "codegen": node "plan": input mapping is empty`; platform.Message != want {
		t.Fatalf("message %q, want %q", platform.Message, want)
	}
}

func TestInvalidStateSchemaRejectedAtLocation(t *testing.T) {
	platform := mustFailure(t, New("codegen", 3).
		StateSchema(json.RawMessage(`{"type":"object","properties":{"tests":{"type":"count"}}}`)))
	if want := `flow "codegen": state schema: invalid schema`; platform.Message != want {
		t.Fatalf("message %q, want %q", platform.Message, want)
	}
	if got := schema.ErrLocation(platform); got != "/properties/tests/type" {
		t.Fatalf("location %q, want /properties/tests/type", got)
	}
}

func TestStateTypeGeneratesSchema(t *testing.T) {
	file := reflect.StructOf([]reflect.StructField{
		{Name: "Path", Type: reflect.TypeFor[string](), Tag: `json:"path"`},
	})
	stateType := reflect.StructOf([]reflect.StructField{
		{Name: "Messages", Type: reflect.TypeFor[[]string](), Tag: `json:"messages"`},
		{Name: "Files", Type: reflect.SliceOf(file), Tag: `json:"files"`},
		{Name: "Attempts", Type: reflect.TypeFor[int](), Tag: `json:"attempts,omitempty"`},
	})
	built := mustBuild(t, New("codegen", 3).
		StateType(stateType).
		Reducer("messages", "append").
		Reducer("files", "merge_by(path)").
		Reducer("attempts", "sum").
		Node("plan", KindAgent, nil).
		Edge(Start, "plan", ""))

	want, err := schema.ForType(stateType)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(built.State.Schema); got != string(want.Raw) {
		t.Fatalf("schema\n got %s\nwant %s", got, want.Raw)
	}
	wantReducers := map[string]string{
		"messages": "append",
		"files":    "merge_by(path)",
		"attempts": "sum",
	}
	if !reflect.DeepEqual(built.State.Reducers, wantReducers) {
		t.Fatalf("reducers %+v, want %+v", built.State.Reducers, wantReducers)
	}
}

func TestBuildIsRepeatableAndIndependent(t *testing.T) {
	b := New("codegen", 3).
		Node("plan", KindAgent, json.RawMessage(`{"agent":"planner"}`)).
		Edge(Start, "plan", "")
	first := mustBuild(t, b)
	second := mustBuild(t, b)
	if got, want := string(mustCanonical(t, second)), string(mustCanonical(t, first)); got != want {
		t.Fatalf("second build differs:\n got %s\nwant %s", got, want)
	}

	first.Nodes["plan"] = flow.Node{Kind: "task"}
	if got := second.Nodes["plan"].Kind; got != "agent" {
		t.Fatalf("builds share the nodes map: second node kind %q", got)
	}
}

func TestDeclarationOrderDoesNotReachCanonicalBytes(t *testing.T) {
	state := json.RawMessage(`{"type":"object","properties":{"a":{},"b":{},"c":{}}}`)
	nodes := []nodeDecl{
		{name: "one", kind: "agent"},
		{name: "two", kind: "tool"},
		{name: "three", kind: "gate"},
	}
	reducers := []reducerDecl{
		{key: "a", reducer: "append"},
		{key: "b", reducer: "sum"},
		{key: "c", reducer: "replace"},
	}
	build := func(nodeOrder, reducerOrder []int) []byte {
		b := New("perm", 1).StateSchema(state)
		for _, i := range reducerOrder {
			b.Reducer(reducers[i].key, reducers[i].reducer)
		}
		for _, i := range nodeOrder {
			b.Node(nodes[i].name, Kind(nodes[i].kind), nil)
		}
		b.Edge(Start, "one", "").Edge("one", "two", "").Edge("two", "three", "").Edge("three", Fail, "")
		return mustCanonical(t, mustBuild(t, b))
	}

	want := string(build([]int{0, 1, 2}, []int{0, 1, 2}))
	for _, nodeOrder := range permutations(3) {
		for _, reducerOrder := range permutations(3) {
			if got := string(build(nodeOrder, reducerOrder)); got != want {
				t.Fatalf("order nodes %v reducers %v changed the bytes:\n got %s\nwant %s",
					nodeOrder, reducerOrder, got, want)
			}
		}
	}
}

func TestInvalidNodeSpecRejected(t *testing.T) {
	platform := mustFailure(t, New("codegen", 3).
		Node("plan", KindAgent, json.RawMessage(`{"agent":`)).
		Edge(Start, "plan", ""))
	if want := `node "plan": invalid node spec`; !strings.Contains(platform.Message, want) {
		t.Fatalf("message %q does not contain %q", platform.Message, want)
	}
}

func TestSpecIsCanonical(t *testing.T) {
	built := mustBuild(t, New("codegen", 1).
		Node("plan", KindAgent, json.RawMessage(`{"b": 2, "a": 1}`)).
		Edge(Start, "plan", ""))
	if got := string(built.Nodes["plan"].Spec); got != `{"a":1,"b":2}` {
		t.Fatalf("spec %s", got)
	}
}

// The builder writes the cycle as declared; guarding it is the compiler's
// check (REQ-FLOW-003), not the builder's.
func TestCyclesAreLeftToTheCompiler(t *testing.T) {
	build := func(guarded bool) *flow.IR {
		var opts []NodeOption
		if guarded {
			opts = append(opts, Guard(flow.Guard{MaxVisits: 1}))
		}
		return mustBuild(t, New("cycle", 1).
			Node("plan", KindAgent, nil, opts...).
			Node("verify", KindTool, nil).
			Edge(Start, "plan", "").
			Edge("plan", "verify", "").
			Edge("verify", "plan", "").
			Edge("verify", Fail, ""))
	}

	if _, err := compile.Compile(build(true)); err != nil {
		t.Fatalf("guarded cycle does not compile: %v", err)
	}
	if _, err := compile.Compile(build(false)); err == nil {
		t.Fatal("unguarded cycle compiled, want the compiler to reject it")
	}
}

func countEdges(f *flow.IR, from, to string) int {
	count := 0
	for _, edge := range f.Edges {
		if edge.From == from && edge.To == to {
			count++
		}
	}
	return count
}

func permutations(n int) [][]int {
	var out [][]int
	current := make([]int, 0, n)
	used := make([]bool, n)
	var walk func()
	walk = func() {
		if len(current) == n {
			out = append(out, append([]int(nil), current...))
			return
		}
		for i := 0; i < n; i++ {
			if used[i] {
				continue
			}
			used[i] = true
			current = append(current, i)
			walk()
			current = current[:len(current)-1]
			used[i] = false
		}
	}
	walk()
	return out
}
