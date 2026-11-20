package expr

import (
	"encoding/json"
	"testing"
)

func TestSchemaPathTypeNameNestedVsDottedProperty(t *testing.T) {
	nested := joinSchemaPath(joinSchemaPath("x", "b"), "c")
	dotted := joinSchemaPath("x", "b.c")
	if nested == dotted {
		t.Fatalf("schema paths collide: %q", nested)
	}
	a := objectTypeNameForSchemaPath(nested)
	b := objectTypeNameForSchemaPath(dotted)
	if a == b {
		t.Fatalf("object type name collision %q", a)
	}
}

func TestNewEnvNestedVsDottedPropertyDistinctTypes(t *testing.T) {
	schema := json.RawMessage(`{
		"type": "object",
		"properties": {
			"b": {
				"type": "object",
				"properties": {
					"c": {
						"type": "object",
						"properties": {"v": {"type": "integer"}}
					}
				}
			},
			"b.c": {
				"type": "object",
				"properties": {"d": {"type": "integer"}}
			}
		}
	}`)
	env, err := NewEnv([]Var{{Name: "x", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	root := env.objects[objectTypeNameForSchemaPath("x")]
	if root == nil {
		t.Fatal("missing root object type")
	}
	nestedName := objectTypeNameForSchemaPath(joinSchemaPath(joinSchemaPath("x", "b"), "c"))
	dottedName := objectTypeNameForSchemaPath(joinSchemaPath("x", "b.c"))
	if nestedName == dottedName {
		t.Fatalf("nested and dotted object types collide: %q", nestedName)
	}
	nestedObj := env.objects[nestedName]
	dottedObj := env.objects[dottedName]
	if nestedObj == nil || dottedObj == nil {
		t.Fatalf("missing nested (%p) or dotted (%p) object type", nestedObj, dottedObj)
	}
	if _, ok := nestedObj.fields["v"]; !ok {
		t.Fatal("nested object missing field v")
	}
	if _, ok := dottedObj.fields["d"]; !ok {
		t.Fatal("dotted object missing field d")
	}
	if _, ok := nestedObj.fields["d"]; ok {
		t.Fatal("nested object must not expose field d from dotted sibling")
	}
	p, err := env.Compile(`x.b.c.v`, nil)
	if err != nil {
		t.Fatalf("compile x.b.c.v: %v", err)
	}
	if got := p.ResultType(); got != "int" {
		t.Fatalf("x.b.c.v result type %q, want int", got)
	}
}
