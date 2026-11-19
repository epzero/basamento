package ir

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
)

func taskNodes(names ...string) map[string]flow.Node {
	nodes := make(map[string]flow.Node, len(names))
	for _, n := range names {
		nodes[n] = flow.Node{Kind: "task"}
	}
	return nodes
}

func simpleFlow() *flow.IR {
	return &flow.IR{
		Name:    "codegen",
		Version: 1,
		State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object","properties":{"tests":{"type":"object"}}}`)},
		Nodes:   taskNodes("plan", "verify"),
		Edges: []flow.Edge{
			{From: Start, To: "plan"},
			{From: "plan", To: "verify"},
			{From: "verify", To: Fail},
		},
	}
}

func TestCanonicalSortsKeysAndNormalizesNumbers(t *testing.T) {
	f := &flow.IR{
		Name:    "canon",
		Version: 1,
		State: flow.StateSpec{
			Schema: json.RawMessage(`{"type":"object","properties":{"b":{"type":"number","default":1.0},"a":{"type":"integer"}}}`),
		},
		Nodes: map[string]flow.Node{
			"z": {Kind: "task", Spec: json.RawMessage(`{"b": 2, "a": 1}`)},
			"a": {Kind: "task"},
		},
		Edges: []flow.Edge{{From: Start, To: "a"}, {From: "a", To: "z"}, {From: "z", To: Fail}},
	}
	got, err := Canonical(f)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.ContainsAny(text, " \n\t") {
		t.Fatalf("canonical form has whitespace: %s", text)
	}
	if strings.Index(text, `"a":{"kind":"task"}`) > strings.Index(text, `"z":{"kind":"task"`) {
		t.Fatalf("nodes are not sorted: %s", text)
	}
	if !strings.Contains(text, `"default":1`) || strings.Contains(text, `"default":1.0`) {
		t.Fatalf("numbers are not canonical: %s", text)
	}
	if !strings.Contains(text, `"spec":{"a":1,"b":2}`) {
		t.Fatalf("node spec is not canonical: %s", text)
	}
}

func TestCanonicalPreservesEdgeOrder(t *testing.T) {
	f := simpleFlow()
	f.Edges = []flow.Edge{
		{From: Start, To: "plan"},
		{From: "plan", To: "verify"},
		{From: "plan", To: Fail},
	}
	got, err := Canonical(f)
	if err != nil {
		t.Fatal(err)
	}
	// An edge list is routing order: START, plan->verify, plan->FAIL.
	want := `"edges":[{"from":"START","to":"plan"},{"from":"plan","to":"verify"},{"from":"plan","to":"FAIL"}]`
	if !strings.Contains(string(got), want) {
		t.Fatalf("edge order changed:\n got %s\nwant %s", got, want)
	}
}

func TestHashIsStableAcrossTextualOrder(t *testing.T) {
	first := `{"name":"x","version":1,"state":{"schema":{"type":"object"}},
		"nodes":{"a":{"kind":"task"},"b":{"kind":"task"}},
		"edges":[{"from":"START","to":"a"},{"from":"a","to":"b"},{"from":"b","to":"FAIL"}]}`
	second := `{"name":"x","version":1,"state":{"schema":{"type":"object"}},
		"nodes":{"b":{"kind":"task"},"a":{"kind":"task"}},
		"edges":[{"from":"START","to":"a"},{"from":"a","to":"b"},{"from":"b","to":"FAIL"}]}`
	h1, err := hashText(first)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := hashText(second)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("hash depends on node order: %s != %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Fatalf("hash is not a 64-char sha256 hex: %q", h1)
	}
}

func TestHashChangesWithEdgeOrder(t *testing.T) {
	f := simpleFlow()
	h1, err := Hash(f)
	if err != nil {
		t.Fatal(err)
	}
	f.Edges[1], f.Edges[2] = f.Edges[2], f.Edges[1]
	h2, err := Hash(f)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("hash ignores edge order, which decides routing")
	}
}

func TestHashChangesWithVersion(t *testing.T) {
	f := simpleFlow()
	h1, err := Hash(f)
	if err != nil {
		t.Fatal(err)
	}
	f.Version++
	h2, err := Hash(f)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Fatal("hash ignores the version")
	}
}

func hashText(text string) (string, error) {
	f, err := Decode([]byte(text))
	if err != nil {
		return "", err
	}
	return Hash(f)
}

func TestDecodeRejectsDuplicateNodeID(t *testing.T) {
	text := `{"name":"x","version":1,"state":{"schema":{"type":"object"}},
		"nodes":{"plan":{"kind":"task"},"plan":{"kind":"tool"}},
		"edges":[{"from":"START","to":"plan"}]}`
	_, err := Decode([]byte(text))
	if err == nil {
		t.Fatal("want error")
	}
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %s", core.CodeOf(err))
	}
	if msg := err.Error(); !strings.Contains(msg, `duplicate node id "plan"`) {
		t.Fatalf("message %q", msg)
	}
}

func TestDecodeRejectsUnknownField(t *testing.T) {
	text := `{"name":"x","version":1,"state":{"schema":{"type":"object"}},"nodes":{},"egdes":[]}`
	_, err := Decode([]byte(text))
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "egdes") {
		t.Fatalf("message %q", err.Error())
	}
}

func TestDecodeAcceptsYAML(t *testing.T) {
	text := `
name: x
version: 2
state:
  schema:
    type: object
    properties:
      tests:
        type: object
nodes:
  plan:
    kind: task
edges:
  - from: START
    to: plan
  - from: plan
    to: FAIL
`
	f, err := Decode([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "x" || f.Version != 2 || len(f.Nodes) != 1 || len(f.Edges) != 2 {
		t.Fatalf("decoded %+v", f)
	}
}

func TestDecodeRejectsDuplicateYAMLKey(t *testing.T) {
	text := `
name: x
version: 1
nodes:
  plan: {kind: task}
  plan: {kind: tool}
`
	if _, err := Decode([]byte(text)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("want duplicate-key error, got %v", err)
	}
}

// A merge key is expanded by YAML after the duplicate check would see it, so a
// key merged through "<<" could otherwise silently override a declared one.
func TestDecodeRejectsYAMLMergeKey(t *testing.T) {
	text := `
name: x
version: 1
state:
  schema: {type: object}
nodes:
  plan: &plan {kind: task}
  copy:
    <<: *plan
    kind: tool
edges:
  - {from: START, to: plan}
`
	_, err := Decode([]byte(text))
	if err == nil {
		t.Fatal("want error")
	}
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %s", core.CodeOf(err))
	}
	if msg := err.Error(); !strings.Contains(msg, `merge key "<<"`) || !strings.Contains(msg, "/nodes/copy") {
		t.Fatalf("message %q", msg)
	}
}

func TestDecodeRejectsJSONMergeKey(t *testing.T) {
	text := `{"name":"x","version":1,"state":{"schema":{"type":"object"}},
		"nodes":{"<<":{"kind":"task"}},
		"edges":[{"from":"START","to":"<<"}]}`
	_, err := Decode([]byte(text))
	if err == nil {
		t.Fatal("want error")
	}
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %s", core.CodeOf(err))
	}
	if msg := err.Error(); !strings.Contains(msg, `merge key "<<"`) || !strings.Contains(msg, "/nodes") {
		t.Fatalf("message %q", msg)
	}
}

func TestDecodeAcceptsFlowStyleYAMLMapping(t *testing.T) {
	text := `{name: x, version: 1, state: {schema: {type: object, properties: {tests: {type: object}}}},
		nodes: {plan: {kind: task}}, edges: [{from: START, to: plan}, {from: plan, to: FAIL}]}`
	f, err := Decode([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != "x" || f.Version != 1 || len(f.Nodes) != 1 || len(f.Edges) != 2 {
		t.Fatalf("decoded %+v", f)
	}
}

func TestDecodeRejectsNonObjectTopLevel(t *testing.T) {
	for _, text := range []string{"null", `[{"name":"x"}]`, `"x"`, "42", "true"} {
		_, err := Decode([]byte(text))
		if err == nil {
			t.Fatalf("%q: want error", text)
		}
		if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("%q: code %s", text, core.CodeOf(err))
		}
	}
}
