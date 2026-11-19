package expr

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMapValueObjectKeepsFieldAccess pins EXPR-003-05: an object type keeps the
// fields of its properties as the value type of a map's additionalProperties,
// whether it is written inline, reached through $ref, or carries
// additionalProperties beside its properties.
func TestMapValueObjectKeepsFieldAccess(t *testing.T) {
	cases := []struct {
		name   string
		schema string
	}{
		{
			name:   "inline",
			schema: `{"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}}}`,
		},
		{
			name:   "through $ref",
			schema: `{"$defs": {"item": {"type": "object", "properties": {"v": {"type": "integer"}}}}, "type": "object", "additionalProperties": {"$ref": "#/$defs/item"}}`,
		},
		{
			name:   "additionalProperties beside properties",
			schema: `{"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}, "additionalProperties": {"type": "string"}}}`,
		},
		{
			name:   "root $ref to a map of objects",
			schema: `{"$defs": {"m": {"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}}}}, "$ref": "#/$defs/m"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := NewEnv([]Var{{Name: "x", Schema: json.RawMessage(tc.schema)}})
			if err != nil {
				t.Fatal(err)
			}
			p, err := env.Compile(`x['k'].v`, nil)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			if got := p.ResultType(); got != "int" {
				t.Fatalf("result type %q, want int", got)
			}
		})
	}
}

// TestMapValueObjectFieldRead pins EXPR-003-06: the field of an object reached
// through additionalProperties is read when the expression is evaluated.
func TestMapValueObjectFieldRead(t *testing.T) {
	schema := json.RawMessage(`{"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}}}`)
	env, err := NewEnv([]Var{{Name: "x", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`x['k'].v + 1`, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Eval(map[string]any{"x": map[string]any{"k": map[string]any{"v": int64(41)}}})
	if err != nil {
		t.Fatal(err)
	}
	if got != int64(42) {
		t.Fatalf("got %v want 42", got)
	}
}

// TestMapValueObjectGuardResult pins EXPR-003-07: a guard over an object
// reached through additionalProperties compiles to bool against a boolean
// result schema.
func TestMapValueObjectGuardResult(t *testing.T) {
	schema := json.RawMessage(`{"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}}}`)
	env, err := NewEnv([]Var{{Name: "x", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`x['k'].v > 0`, json.RawMessage(`{"type": "boolean"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.ResultType(); got != "bool" {
		t.Fatalf("result type %q, want bool", got)
	}
}

// TestNestedMapValueObjectFieldAccess pins that an object keeps its fields
// when it is a map value inside a list or another map, not only at the top
// level of the schema.
func TestNestedMapValueObjectFieldAccess(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		src    string
		bind   map[string]any
		want   any
	}{
		{
			name:   "list of maps of objects",
			schema: `{"type": "object", "properties": {"rows": {"type": "array", "items": {"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}}}}}}`,
			src:    `x.rows[0]['k'].v`,
			bind:   map[string]any{"x": map[string]any{"rows": []any{map[string]any{"k": map[string]any{"v": int64(7)}}}}},
			want:   int64(7),
		},
		{
			name:   "map of maps of objects",
			schema: `{"type": "object", "additionalProperties": {"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}}}}`,
			src:    `x['a']['b'].v`,
			bind:   map[string]any{"x": map[string]any{"a": map[string]any{"b": map[string]any{"v": int64(9)}}}},
			want:   int64(9),
		},
		{
			name:   "root $ref to a map of objects",
			schema: `{"$defs": {"m": {"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}}}}, "$ref": "#/$defs/m"}`,
			src:    `x['k'].v`,
			bind:   map[string]any{"x": map[string]any{"k": map[string]any{"v": int64(11)}}},
			want:   int64(11),
		},
		{
			name:   "root $ref to a list of maps of objects",
			schema: `{"$defs": {"rows": {"type": "array", "items": {"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}}}}}, "$ref": "#/$defs/rows"}`,
			src:    `x[0]['k'].v`,
			bind:   map[string]any{"x": []any{map[string]any{"k": map[string]any{"v": int64(13)}}}},
			want:   int64(13),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, err := NewEnv([]Var{{Name: "x", Schema: json.RawMessage(tc.schema)}})
			if err != nil {
				t.Fatal(err)
			}
			p, err := env.Compile(tc.src, nil)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			got, err := p.Eval(tc.bind)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

// TestMapValueNonObjectHasNoFieldAccess pins the other half of EXPR-003-05: a
// non-object value type gives the plain map(string, T(S)) whose lookup has no
// field access.
func TestMapValueNonObjectHasNoFieldAccess(t *testing.T) {
	env, err := NewEnv([]Var{{Name: "x", Schema: json.RawMessage(`{"type": "object", "additionalProperties": {"type": "integer"}}`)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.Compile(`x['k'].v`, nil); err == nil {
		t.Fatal("compiled field access on a map of int")
	}
}

// TestMapValueObjectUndeclaredPropertyRejected pins that the map value object
// keeps its type error for a property it does not declare, and that an
// additionalProperties beside its properties adds no field (design.md §2).
func TestMapValueObjectUndeclaredPropertyRejected(t *testing.T) {
	schemas := []string{
		`{"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}}}`,
		`{"type": "object", "additionalProperties": {"type": "object", "properties": {"v": {"type": "integer"}}, "additionalProperties": {"type": "string"}}}`,
	}
	for _, schema := range schemas {
		env, err := NewEnv([]Var{{Name: "x", Schema: json.RawMessage(schema)}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = env.Compile(`x['k'].missing`, nil)
		if err == nil {
			t.Fatalf("%s: compiled an undeclared property on a map value object", schema)
		}
		if !strings.Contains(err.Error(), `undeclared property "missing"`) {
			t.Fatalf("%s: error %q does not name the undeclared property", schema, err)
		}
	}
}
