package schema

import (
	"encoding/json"
	"testing"
)

func TestFalseSchemaViolationAtRoot(t *testing.T) {
	c, err := Compile(json.RawMessage(`false`))
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate(json.RawMessage(`null`))
	vs := Violations(err)
	if len(vs) != 1 || vs[0].Location != "" || vs[0].Keyword != "false" {
		t.Fatalf("violations %+v", vs)
	}
}

func TestNotViolationEmitted(t *testing.T) {
	c, err := Compile(json.RawMessage(`{"not":{"type":"string"}}`))
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate(json.RawMessage(`"hi"`))
	vs := Violations(err)
	if len(vs) != 1 || vs[0].Keyword != "not" {
		t.Fatalf("violations %+v", vs)
	}
}

func TestDependentRequiredKeyword(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","dependentRequired":{"a":["b"]}}`)
	c, err := Compile(raw)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate(json.RawMessage(`{"a":1}`))
	vs := Violations(err)
	if len(vs) != 1 || vs[0].Keyword != "dependentRequired" {
		t.Fatalf("violations %+v", vs)
	}
}

func TestDynamicRefUnsupportedFormatStrips(t *testing.T) {
	raw := json.RawMessage(`{
		"$dynamicAnchor": "items",
		"items": {"$dynamicRef": "#items", "format": "ipv4"},
		"$defs": {"items": {"type": "string"}}
	}`)
	_, err := Compile(raw)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
}