package expr

import (
	"encoding/json"
	"testing"
)

// TestRecursiveAdditionalPropertiesNoOverflow pins that a definition whose
// additionalProperties refers back to it maps to map(string, dyn) at the
// recursion point instead of expanding without end.
func TestRecursiveAdditionalPropertiesNoOverflow(t *testing.T) {
	schema := json.RawMessage(`{
		"$defs": {"node": {"type": "object", "additionalProperties": {"$ref": "#/$defs/node"}}},
		"$ref": "#/$defs/node"
	}`)
	env, err := NewEnv([]Var{{Name: "tree", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`tree`, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"a": map[string]any{"b": map[string]any{}}}
	res, err := p.Eval(map[string]any{"tree": want})
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEqual(t, res, want)
}

// TestNonRootRecursiveDefNoOverflow pins that a self-referential definition
// reached from a property, not from the schema root, stops at dyn instead of
// recursing forever in buildObjectType.
func TestNonRootRecursiveDefNoOverflow(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {"child": {"$ref": "#/$defs/node"}},
		"$defs": {"node": {
			"type": "object",
			"properties": {
				"v": {"type": "integer"},
				"next": {"$ref": "#/$defs/node"}
			}
		}}
	}`)
	env, err := NewEnv([]Var{{Name: "state", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`state.child.v`, nil)
	if err != nil {
		t.Fatal(err)
	}
	bind := map[string]any{"state": map[string]any{
		"child": map[string]any{
			"v":    int64(1),
			"next": map[string]any{"v": int64(2)},
		},
	}}
	res, err := p.Eval(bind)
	if err != nil {
		t.Fatal(err)
	}
	if res != int64(1) {
		t.Fatalf("got %v want 1", res)
	}
	// The recursion point is dyn, so the nested object still reads normally.
	p, err = env.Compile(`state.child.next.v`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = p.Eval(bind)
	if err != nil {
		t.Fatal(err)
	}
	if res != int64(2) {
		t.Fatalf("got %v want 2", res)
	}
}

// TestChainedRefObjectFields pins that objectSchemaForBuild follows every
// $ref hop, including through array items, so the built object type keeps its
// declared fields.
func TestChainedRefObjectFields(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"node": {"$ref": "#/$defs/alias"},
			"list": {"type": "array", "items": {"$ref": "#/$defs/alias"}}
		},
		"$defs": {
			"alias": {"$ref": "#/$defs/node"},
			"node": {"type": "object", "properties": {"v": {"type": "integer"}}}
		}
	}`)
	env, err := NewEnv([]Var{{Name: "state", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	bind := map[string]any{"state": map[string]any{
		"node": map[string]any{"v": int64(1)},
		"list": []any{map[string]any{"v": int64(2)}},
	}}
	p, err := env.Compile(`state.node.v`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(bind)
	if err != nil {
		t.Fatal(err)
	}
	if res != int64(1) {
		t.Fatalf("got %v want 1", res)
	}
	p, err = env.Compile(`state.list[0].v`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err = p.Eval(bind)
	if err != nil {
		t.Fatal(err)
	}
	if res != int64(2) {
		t.Fatalf("got %v want 2", res)
	}
}

// TestRootChainedRefObjectFields covers the same chain at the schema root.
func TestRootChainedRefObjectFields(t *testing.T) {
	schema := json.RawMessage(`{
		"$defs": {
			"alias": {"$ref": "#/$defs/node"},
			"node": {"type": "object", "properties": {"v": {"type": "integer"}}}
		},
		"$ref": "#/$defs/alias"
	}`)
	env, err := NewEnv([]Var{{Name: "s", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`s.v`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(map[string]any{"s": map[string]any{"v": int64(7)}})
	if err != nil {
		t.Fatal(err)
	}
	if res != int64(7) {
		t.Fatalf("got %v want 7", res)
	}
}

// TestRootArrayOfObjectRefFields pins that a root $ref to an array of objects
// builds the element object type with its fields.
func TestRootArrayOfObjectRefFields(t *testing.T) {
	schema := json.RawMessage(`{
		"$defs": {"list": {"type": "array", "items": {"type": "object", "properties": {"v": {"type": "integer"}}}}},
		"$ref": "#/$defs/list"
	}`)
	env, err := NewEnv([]Var{{Name: "xs", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`xs[0].v`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(map[string]any{"xs": []any{map[string]any{"v": int64(7)}}})
	if err != nil {
		t.Fatal(err)
	}
	if res != int64(7) {
		t.Fatalf("got %v want 7", res)
	}
}
