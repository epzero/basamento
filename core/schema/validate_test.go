package schema

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestCompileAndValidatePrefixItems(t *testing.T) {
	raw := json.RawMessage(`{"type": "array", "prefixItems": [{"type": "string"}, {"type": "integer"}]}`)
	c, err := Compile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(json.RawMessage(`["a", 1]`)); err != nil {
		t.Fatalf("accept: %v", err)
	}
	err = c.Validate(json.RawMessage(`["a", "b"]`))
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %v", core.CodeOf(err))
	}
	vs := Violations(err)
	if len(vs) != 1 || vs[0].Location != "/1" || vs[0].Keyword != "type" {
		t.Fatalf("violations %+v", vs)
	}
}

func TestRequiredViolationAtProperty(t *testing.T) {
	raw := json.RawMessage(`{"type": "object", "properties": {"approved": {"type": "boolean"}}, "required": ["approved"], "additionalProperties": false}`)
	c, err := Compile(raw)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate(json.RawMessage(`{}`))
	vs := Violations(err)
	if len(vs) != 1 || vs[0].Location != "/approved" || vs[0].Keyword != "required" {
		t.Fatalf("violations %+v", vs)
	}
}

func TestDuplicateInstanceMember(t *testing.T) {
	raw := json.RawMessage(`{"type": "object", "properties": {"approved": {"const": true}}}`)
	c, err := Compile(raw)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate(json.RawMessage(`{"approved": true, "approved": false}`))
	if ErrLocation(err) != "/approved" {
		t.Fatalf("location %q", ErrLocation(err))
	}
}
