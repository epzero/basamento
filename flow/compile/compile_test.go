package compile

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
	"github.com/epzero/basamento/flow"
	"github.com/epzero/basamento/flow/ir"
)

func typedFlow() *flow.IR {
	return &flow.IR{
		Name:    "triage",
		Version: 1,
		State: flow.StateSpec{
			Schema: json.RawMessage(`{"type":"object","properties":{"failed":{"type":"integer"},"done":{"type":"boolean"},"items":{"type":"array","items":{"type":"object"}}}}`),
		},
		Nodes: map[string]flow.Node{
			"plan":  {Kind: "task"},
			"check": {Kind: "task"},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "plan"},
			{From: "plan", To: "check"},
			{From: "check", To: ir.Fail},
		},
	}
}

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

func TestCompileRejectsEdgeConditionThatDoesNotTypeCheck(t *testing.T) {
	f := typedFlow()
	f.Edges[1].When = "state.failed == 'none'"
	_, err := Compile(f)
	requireInvalid(t, err,
		`edge "plan" -> "check"`,
		`expression "state.failed == 'none'"`,
	)
}

func TestCompileAcceptsEdgeConditionOverTheStateSchema(t *testing.T) {
	f := typedFlow()
	f.Edges[1].When = "state.failed == 0"
	if _, err := Compile(f); err != nil {
		t.Fatal(err)
	}
}

func TestCompileRejectsUnknownStateField(t *testing.T) {
	f := typedFlow()
	f.Edges[1].When = "state.missing == 0"
	requireInvalid(t, errOf(t, f), `edge "plan" -> "check"`, "missing")
}

func TestCompileRejectsGuardConditionThatDoesNotTypeCheck(t *testing.T) {
	f := typedFlow()
	node := f.Nodes["check"]
	node.Guard = &flow.Guard{MaxVisits: 3, While: "state.failed"}
	f.Nodes["check"] = node
	requireInvalid(t, errOf(t, f), `node "check"`, "guard", `expression "state.failed"`)
}

func TestCompileRejectsNodeInputThatIsNeitherCELNorJQ(t *testing.T) {
	f := typedFlow()
	node := f.Nodes["check"]
	node.In = "state.failed =="
	f.Nodes["check"] = node
	err := errOf(t, f)
	requireInvalid(t, err, `node "check"`, "input")
	// Neither parser's message is dropped: each says something different.
	if !strings.Contains(err.Error(), "not CEL") || !strings.Contains(err.Error(), "not jq") {
		t.Fatalf("message %q does not keep both parsers' errors", err)
	}
}

func TestCompileRejectsGateAcceptIfThatDoesNotTypeCheck(t *testing.T) {
	f := typedFlow()
	node := f.Nodes["check"]
	node.Kind = "gate"
	node.Spec = json.RawMessage(`{"accept_if":"state.nope == 1"}`)
	f.Nodes["check"] = node
	requireInvalid(t, errOf(t, f), `node "check" gate accept_if`, "nope")
}

func TestCompileRejectsGateAcceptIfThatIsNotBoolean(t *testing.T) {
	f := typedFlow()
	node := f.Nodes["check"]
	node.Kind = "gate"
	node.Spec = json.RawMessage(`{"accept_if":"state.failed"}`)
	f.Nodes["check"] = node
	requireInvalid(t, errOf(t, f), `node "check" gate accept_if`, "bool")
}

func TestCompileRejectsMapOverThatDoesNotTypeCheck(t *testing.T) {
	f := typedFlow()
	node := f.Nodes["check"]
	node.Kind = "map"
	node.Spec = json.RawMessage(`{"over":"state.nope","node":"impl"}`)
	f.Nodes["check"] = node
	requireInvalid(t, errOf(t, f), `node "check" map over`, "nope")
}

func TestCompileAcceptsGateAcceptIfAndMapOver(t *testing.T) {
	for _, spec := range []struct {
		kind string
		spec string
	}{
		{kind: "gate", spec: `{"accept_if":"state.done"}`},
		{kind: "gate", spec: `{"schema":"review.json","accept_if":"state.failed == 0","timeout":"72h"}`},
		{kind: "map", spec: `{"over":"state.items","node":"impl","max_parallel":4}`},
	} {
		f := typedFlow()
		node := f.Nodes["check"]
		node.Kind = spec.kind
		node.Spec = json.RawMessage(spec.spec)
		f.Nodes["check"] = node
		if _, err := Compile(f); err != nil {
			t.Fatalf("%s spec %s: %v", spec.kind, spec.spec, err)
		}
	}
}

func TestCompileAcceptsNodeInputCELAndJQ(t *testing.T) {
	for _, in := range []string{`{"failed": state.failed}`, `.failed`} {
		f := typedFlow()
		node := f.Nodes["check"]
		node.In = in
		f.Nodes["check"] = node
		if _, err := Compile(f); err != nil {
			t.Fatalf("input %q: %v", in, err)
		}
	}
}

func TestCompileReportsStructuralErrorsFirst(t *testing.T) {
	f := typedFlow()
	f.Nodes["START"] = flow.Node{Kind: "task"}
	f.Edges[1].When = "state.failed == 'none'"
	requireInvalid(t, errOf(t, f), `node "START"`, "reserved")
}

func TestCompileReturnsTheCanonicalIRAndItsHash(t *testing.T) {
	f := typedFlow()
	c, err := Compile(f)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := ir.Canonical(f)
	if err != nil {
		t.Fatal(err)
	}
	if string(c.Canonical()) != string(canonical) {
		t.Fatalf("compiled bytes differ from the canonical IR:\n%s\n%s", c.Canonical(), canonical)
	}
	hash, err := ir.Hash(f)
	if err != nil {
		t.Fatal(err)
	}
	if c.Hash() != hash {
		t.Fatalf("hash %q != %q", c.Hash(), hash)
	}
	if c.IR() != f {
		t.Fatal("compiled flow does not carry the compiled IR")
	}
}

func TestCompileIsDeterministicAcrossDeclarationOrder(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e"}
	var firstHash string
	var firstBytes string
	rnd := rand.New(rand.NewSource(7)) // seeded: the test reproduces the same orders
	for i := 0; i < 30; i++ {
		nodes := map[string]flow.Node{}
		for _, j := range rnd.Perm(len(names)) {
			nodes[names[j]] = flow.Node{Kind: "task"}
		}
		f := &flow.IR{
			Name:    "det",
			Version: 1,
			State:   flow.StateSpec{Schema: json.RawMessage(`{"type":"object"}`)},
			Nodes:   nodes,
			Edges: []flow.Edge{
				{From: ir.Start, To: "a"},
				{From: "a", To: "b"},
				{From: "b", To: "c"},
				{From: "c", To: "d"},
				{From: "d", To: "e"},
				{From: "e", To: ir.Fail},
			},
		}
		c, err := Compile(f)
		if err != nil {
			t.Fatal(err)
		}
		if firstHash == "" {
			firstHash, firstBytes = c.Hash(), string(c.Canonical())
			continue
		}
		if c.Hash() != firstHash || string(c.Canonical()) != firstBytes {
			t.Fatalf("iteration %d: compiled bytes depend on declaration order", i)
		}
	}
}

func TestCompileJSON(t *testing.T) {
	text := `{
		"name":"triage","version":1,
		"state":{"schema":{"type":"object","properties":{"failed":{"type":"integer"}}}},
		"nodes":{"plan":{"kind":"task"},"check":{"kind":"task"}},
		"edges":[
			{"from":"START","to":"plan"},
			{"from":"plan","to":"check","when":"state.failed == 0"},
			{"from":"check","to":"FAIL"}
		]
	}`
	c, err := CompileJSON([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if c.Hash() == "" || !strings.Contains(string(c.Canonical()), `"when":"state.failed == 0"`) {
		t.Fatalf("compiled %s", c.Canonical())
	}
}

func TestCompileJSONRejectsDuplicateNode(t *testing.T) {
	text := `{"name":"x","version":1,"state":{"schema":{"type":"object"}},
		"nodes":{"plan":{"kind":"task"},"plan":{"kind":"tool"}},
		"edges":[{"from":"START","to":"plan"}]}`
	_, err := CompileJSON([]byte(text))
	requireInvalid(t, err, `duplicate node id "plan"`)
}

func errOf(t *testing.T, f *flow.IR) error {
	t.Helper()
	_, err := Compile(f)
	return err
}
