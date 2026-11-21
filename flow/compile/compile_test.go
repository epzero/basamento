package compile

import (
	"encoding/json"
	"math/rand"
	"os"
	"path/filepath"
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

// A map starts its child from spec.node, so the arc from the map to its child
// is implicit and the edge list does not carry it. A child that routes back to
// its map is a loop the machine runs for ever, and the compiler must judge it
// like any other cycle (REQ-FLOW-003).
func TestCompileRejectsAnUnguardedMapChildCycle(t *testing.T) {
	requireInvalid(t, errOf(t, mapChildCycleFlow()), `node "fanout"`, "guard")
}

// The same loop with a bounded node on it compiles: the map's guard counts
// the visits of the map, which the machine routes to.
func TestCompileAcceptsAGuardedMapChildCycle(t *testing.T) {
	f := mapChildCycleFlow()
	node := f.Nodes["fanout"]
	node.Guard = &flow.Guard{MaxVisits: 3}
	f.Nodes["fanout"] = node
	if _, err := Compile(f); err != nil {
		t.Fatal(err)
	}
}

// mapChildCycleFlow is a flow whose only loop is the implicit arc from the
// map to its child and the edge from the child back to the map.
func mapChildCycleFlow() *flow.IR {
	return &flow.IR{
		Name:    "map-cycle",
		Version: 1,
		State: flow.StateSpec{
			Schema: json.RawMessage(`{"type":"object","properties":{"items":{"type":"array","items":{"type":"object"}}}}`),
		},
		Nodes: map[string]flow.Node{
			"fanout":      {Kind: "map", Spec: json.RawMessage(`{"over":"state.items","node":"fanout_item"}`)},
			"fanout_item": {Kind: "task"},
		},
		Edges: []flow.Edge{
			{From: ir.Start, To: "fanout"},
			{From: "fanout_item", To: "fanout"},
		},
	}
}

func TestCompileRejectsMapOverThatDoesNotTypeCheck(t *testing.T) {
	f := typedFlow()
	node := f.Nodes["check"]
	node.Kind = "map"
	node.Spec = json.RawMessage(`{"over":"state.nope","node":"impl"}`)
	f.Nodes["check"] = node
	f.Nodes["impl"] = flow.Node{Kind: "task"}
	f.Edges = append(f.Edges, flow.Edge{From: "check", To: "impl"})
	requireInvalid(t, errOf(t, f), `node "check" map over`, "nope")
}

// A spec is a JSON object (specs/flow-engine/design.md §1): one the compiler
// cannot read is invalid_argument, not a node without expressions.
func TestCompileRejectsASpecThatIsNotAJSONObject(t *testing.T) {
	for _, spec := range []string{`true`, `null`, `[]`, `"gate"`, `1`} {
		for _, kind := range []string{"gate", "task"} {
			f := typedFlow()
			node := f.Nodes["check"]
			node.Kind = kind
			node.Spec = json.RawMessage(spec)
			f.Nodes["check"] = node
			requireInvalid(t, errOf(t, f), `node "check" spec`, "not a JSON object")
		}
	}
}

func TestCompileRejectsASpecExpressionThatIsNotAString(t *testing.T) {
	// A map's "over" is checked by ir.Validate's shape rule first (EP0-166,
	// F1): a null or non-string one is rejected there, not here.
	tests := []struct {
		kind, spec, subject string
	}{
		{kind: "gate", spec: `{"accept_if":true}`, subject: `node "check" gate accept_if`},
		{kind: "gate", spec: `{"accept_if":1}`, subject: `node "check" gate accept_if`},
		{kind: "gate", spec: `{"accept_if":null}`, subject: `node "check" gate accept_if`},
		{kind: "gate", spec: `{"accept_if":{"expr":"state.done"}}`, subject: `node "check" gate accept_if`},
		{kind: "gate", spec: `{"accept_if":["state.done"]}`, subject: `node "check" gate accept_if`},
	}
	for _, test := range tests {
		f := typedFlow()
		node := f.Nodes["check"]
		node.Kind = test.kind
		node.Spec = json.RawMessage(test.spec)
		f.Nodes["check"] = node
		requireInvalid(t, errOf(t, f), test.subject, "not a string")
	}
}

// An empty string is no expression, not an unreadable one, for a gate. A
// map's "over" is the exception: the machine needs it, so the compiler
// rejects an empty one before the expression check (EP0-166).
func TestCompileAcceptsEmptyGateAcceptIfAndRejectsEmptyMapOver(t *testing.T) {
	f := typedFlow()
	node := f.Nodes["check"]
	node.Kind = "gate"
	node.Spec = json.RawMessage(`{"accept_if":""}`)
	f.Nodes["check"] = node
	if _, err := Compile(f); err != nil {
		t.Fatalf("gate accept_if: %v", err)
	}

	f = typedFlow()
	node = f.Nodes["check"]
	node.Kind = "map"
	node.Spec = json.RawMessage(`{"over":"","node":"impl"}`)
	f.Nodes["check"] = node
	f.Nodes["impl"] = flow.Node{Kind: "task"}
	f.Edges = append(f.Edges, flow.Edge{From: "check", To: "impl"})
	requireInvalid(t, errOf(t, f), `map node "check"`, `"over"`, "non-empty")
}

// A whitespace-only "over" keeps the shape of an expression, so ir.Validate
// accepts it and the expression compiler rejects it here; flow/machine reaches
// the same verdict at New (EP0-166, F2).
func TestCompileRejectsWhitespaceOnlyMapOverAtTheExpressionStage(t *testing.T) {
	f := typedFlow()
	node := f.Nodes["check"]
	node.Kind = "map"
	node.Spec = json.RawMessage(`{"over":"   ","node":"impl"}`)
	f.Nodes["check"] = node
	f.Nodes["impl"] = flow.Node{Kind: "task"}
	f.Edges = append(f.Edges, flow.Edge{From: "check", To: "impl"})
	err := errOf(t, f)
	requireInvalid(t, err, `node "check" map over`)
	if strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("error %q: whitespace-only over was rejected as a shape, want the expression stage", err)
	}
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
		if spec.kind == "map" {
			f.Nodes["impl"] = flow.Node{Kind: "task"}
			f.Edges = append(f.Edges, flow.Edge{From: "check", To: "impl"})
		}
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

// longCondition returns a CEL condition of exactly n bytes that type-checks:
// "true" joined by " || true", padded with trailing spaces. The size limit is a
// property of the bytes, not of the parse tree.
func longCondition(n int) string {
	const unit = " || true"
	s := "true"
	for len(s)+len(unit) <= n {
		s += unit
	}
	return s + strings.Repeat(" ", n-len(s))
}

func TestCompileAcceptsAConditionAtItsMaximum(t *testing.T) {
	f := typedFlow()
	f.Edges[1].When = longCondition(ir.MaxExpressionBytes)
	c, err := Compile(f)
	if err != nil {
		t.Fatalf("a %d-byte condition did not compile: %v", ir.MaxExpressionBytes, err)
	}
	if got := c.IR().Edges[1].When; len(got) != ir.MaxExpressionBytes {
		t.Fatalf("condition is %d bytes, want %d", len(got), ir.MaxExpressionBytes)
	}
}

func TestCompileRejectsAConditionAboveItsMaximum(t *testing.T) {
	f := typedFlow()
	f.Edges[1].When = longCondition(ir.MaxExpressionBytes + 1)
	requireInvalid(t, errOf(t, f), "expression size limit of 16384 bytes exceeded", `edge "plan" -> "check" when`)
}

func TestCompileAcceptsADefinitionAtItsMaximum(t *testing.T) {
	f := typedFlow()
	padDefinition(t, f, ir.MaxDefinitionBytes)
	c, err := Compile(f)
	if err != nil {
		t.Fatalf("a definition at the size limit did not compile: %v", err)
	}
	if got := len(c.Canonical()); got != ir.MaxDefinitionBytes {
		t.Fatalf("canonical definition is %d bytes, want %d", got, ir.MaxDefinitionBytes)
	}
}

func TestCompileRejectsADefinitionAboveItsMaximum(t *testing.T) {
	f := typedFlow()
	padDefinition(t, f, ir.MaxDefinitionBytes+1)
	requireInvalid(t, errOf(t, f), "definition size limit of 1048576 bytes exceeded", "1048577 bytes")
}

// padDefinition grows f's defaults until its canonical JSON is exactly size
// bytes. A JSON string adds one canonical byte per character, so the loop
// converges in two passes whatever the key's own overhead is.
func padDefinition(t *testing.T, f *flow.IR, size int) {
	t.Helper()
	if f.Defaults == nil {
		f.Defaults = map[string]json.RawMessage{"padding": nil}
	}
	pad := 0
	for i := 0; i < 5; i++ {
		f.Defaults["padding"] = json.RawMessage(`"` + strings.Repeat("a", pad) + `"`)
		canonical, err := ir.Canonical(f)
		if err != nil {
			t.Fatal(err)
		}
		delta := size - len(canonical)
		if delta == 0 {
			return
		}
		if pad+delta < 0 {
			t.Fatalf("cannot pad a definition to %d bytes", size)
		}
		pad += delta
	}
	t.Fatalf("could not pad the definition to %d bytes", size)
}

func errOf(t *testing.T, f *flow.IR) error {
	t.Helper()
	_, err := Compile(f)
	return err
}

// TestDocumentedExampleCompiles keeps the canonical IR in
// docs/architecture/flow-engine.md §1 honest: prose that stops compiling fails
// here, and the example cannot drift from what the compiler accepts.
func TestDocumentedExampleCompiles(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "architecture", "flow-engine.md"))
	if err != nil {
		t.Fatal(err)
	}
	example := fencedBlock(t, string(doc), "yaml")
	c, err := CompileJSON([]byte(example))
	if err != nil {
		t.Fatalf("the documented example does not compile: %v", err)
	}
	if c.Hash() == "" || len(c.IR().Nodes) != 6 {
		t.Fatalf("compiled the documented example to %d nodes", len(c.IR().Nodes))
	}
}

func fencedBlock(t *testing.T, doc, language string) string {
	t.Helper()
	opening := "```" + language + "\n"
	start := strings.Index(doc, opening)
	if start < 0 {
		t.Fatalf("no %q fenced block in the architecture document", language)
	}
	rest := doc[start+len(opening):]
	end := strings.Index(rest, "```")
	if end < 0 {
		t.Fatalf("unterminated %q fenced block", language)
	}
	return rest[:end]
}

// A state schema the expression layer cannot read gets one verdict: it fails
// for a flow with expressions and for one without, because the environment is
// built either way. A boolean false schema is valid JSON Schema and admits no
// value, so expr cannot type-check anything against it.
func TestCompileGivesOneVerdictPerStateSchema(t *testing.T) {
	for _, when := range []string{"", "true"} {
		f := typedFlow()
		f.State.Schema = json.RawMessage(`false`)
		f.Edges[1].When = when
		requireInvalid(t, errOf(t, f), `variable "state"`, "invalid schema")
	}
}
