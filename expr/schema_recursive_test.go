package expr

import (
	"encoding/json"
	"strings"
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

// TestRootArrayOfInlineObjectFields pins that a root array whose items are an
// inline object builds the element type from the items document, not from the
// array document: design.md §2 maps it to list(object{v:int}).
func TestRootArrayOfInlineObjectFields(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "array",
		"items": {"type": "object", "properties": {"v": {"type": "integer"}}, "required": ["v"]}
	}`)
	env, err := NewEnv([]Var{{Name: "xs", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`xs[0].v`, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	res, err := p.Eval(map[string]any{"xs": []any{map[string]any{"v": int64(7)}}})
	if err != nil {
		t.Fatal(err)
	}
	if res != int64(7) {
		t.Fatalf("got %v want 7", res)
	}
}

// TestRootNestedArrayOfInlineObjectFields pins the same normalization through
// nested inline arrays.
func TestRootNestedArrayOfInlineObjectFields(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "array",
		"items": {"type": "array", "items": {"type": "object", "properties": {"v": {"type": "integer"}}}}
	}`)
	env, err := NewEnv([]Var{{Name: "xs", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`xs[0][0].v`, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	res, err := p.Eval(map[string]any{"xs": []any{[]any{map[string]any{"v": int64(7)}}}})
	if err != nil {
		t.Fatal(err)
	}
	if res != int64(7) {
		t.Fatalf("got %v want 7", res)
	}
}

// TestCyclicPureRefAliasIsDyn pins that a $ref chain whose definitions only
// point at other $refs and loop back on itself declares no type: every
// definition refers back to itself, so the type at that point is dyn
// (design.md §2) and NewEnv builds instead of rejecting the schema. The chain
// is reached at the schema root, as an object field and as a map value, the
// three places NewEnv follows a $ref while registering nested objects.
func TestCyclicPureRefAliasIsDyn(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		src    string
		bind   map[string]any
		want   any
	}{
		{
			name:   "alias chain a to b to a at the root",
			schema: `{"$defs": {"a": {"$ref": "#/$defs/b"}, "b": {"$ref": "#/$defs/a"}}, "$ref": "#/$defs/a"}`,
			src:    `x`,
			bind:   map[string]any{"x": int64(3)},
			want:   int64(3),
		},
		{
			name:   "self alias at the root",
			schema: `{"$defs": {"a": {"$ref": "#/$defs/a"}}, "$ref": "#/$defs/a"}`,
			src:    `x`,
			bind:   map[string]any{"x": "v"},
			want:   "v",
		},
		{
			name:   "alias chain as an object field",
			schema: `{"type": "object", "properties": {"p": {"$ref": "#/$defs/a"}}, "$defs": {"a": {"$ref": "#/$defs/b"}, "b": {"$ref": "#/$defs/a"}}}`,
			src:    `x.p.v`,
			bind:   map[string]any{"x": map[string]any{"p": map[string]any{"v": int64(5)}}},
			want:   int64(5),
		},
		{
			name:   "alias chain as a map value",
			schema: `{"type": "object", "additionalProperties": {"$ref": "#/$defs/a"}, "$defs": {"a": {"$ref": "#/$defs/b"}, "b": {"$ref": "#/$defs/a"}}}`,
			src:    `x['k'].v`,
			bind:   map[string]any{"x": map[string]any{"k": map[string]any{"v": int64(7)}}},
			want:   int64(7),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := NewEnv([]Var{{Name: "x", Schema: json.RawMessage(tc.schema)}})
			if err != nil {
				t.Fatalf("NewEnv: %v", err)
			}
			p, err := env.Compile(tc.src, nil)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			got, err := p.Eval(tc.bind)
			if err != nil {
				t.Fatalf("eval: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

// TestCyclicPureRefAliasRootTypeIsDyn pins the type of the two root cases: the
// variable whose schema is a cyclic $ref chain is dyn, as a definition that
// refers back to itself is (design.md §2).
func TestCyclicPureRefAliasRootTypeIsDyn(t *testing.T) {
	schemas := []string{
		`{"$defs": {"a": {"$ref": "#/$defs/b"}, "b": {"$ref": "#/$defs/a"}}, "$ref": "#/$defs/a"}`,
		`{"$defs": {"a": {"$ref": "#/$defs/a"}}, "$ref": "#/$defs/a"}`,
	}
	for _, schema := range schemas {
		env, err := NewEnv([]Var{{Name: "x", Schema: json.RawMessage(schema)}})
		if err != nil {
			t.Fatalf("%s: NewEnv: %v", schema, err)
		}
		p, err := env.Compile(`x`, nil)
		if err != nil {
			t.Fatalf("%s: compile: %v", schema, err)
		}
		if got := p.ResultType(); got != "dyn" {
			t.Fatalf("%s: result type %q, want dyn", schema, got)
		}
	}
}

// TestDeeplyNestedObjectSchemaBuildsOnce pins that NewEnv builds a nested object
// schema in time linear in its size. It used to grow exponentially with the
// nesting level twice over: registerNestedObjects descended into every property
// itself and then again through buildObjectType, and buildObjectType named a
// nested type after its parent's type name instead of its schema path, so every
// level re-escaped the level above and doubled the name. No clock is asserted:
// an exponential build of 40 levels never finishes, a linear one is instant.
func TestDeeplyNestedObjectSchemaBuildsOnce(t *testing.T) {
	const depth = 40
	env, err := NewEnv([]Var{{Name: "x", Schema: json.RawMessage(nestedObjectSchema(depth))}})
	if err != nil {
		t.Fatalf("NewEnv: %v", err)
	}
	src := "x" + strings.Repeat(".child", depth) + ".v"
	p, err := env.Compile(src, nil)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if got := p.ResultType(); got != "int" {
		t.Fatalf("result type %q, want int", got)
	}
	var bind any = map[string]any{"v": int64(9)}
	for i := 0; i < depth; i++ {
		bind = map[string]any{"v": int64(i), "child": bind}
	}
	got, err := p.Eval(map[string]any{"x": bind})
	if err != nil {
		t.Fatalf("eval: %v", err)
	}
	if got != int64(9) {
		t.Fatalf("got %v want 9", got)
	}
}

// nestedObjectSchema builds an object schema nested depth levels deep: each
// level declares an int property and a child object, the innermost only the
// int property.
func nestedObjectSchema(depth int) string {
	var b strings.Builder
	for i := 0; i < depth; i++ {
		b.WriteString(`{"type": "object", "properties": {"v": {"type": "integer"}, "child": `)
	}
	b.WriteString(`{"type": "object", "properties": {"v": {"type": "integer"}}}`)
	for i := 0; i < depth; i++ {
		b.WriteString(`}}`)
	}
	return b.String()
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
