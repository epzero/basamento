package schema

import (
	"encoding/json"
	"strings"
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

func TestViolationMessagesOmitInstanceSecrets(t *testing.T) {
	const secret = "super-secret-token-xyzzy"
	raw := json.RawMessage(`{"type":"string","pattern":"^[a-z]+$"}`)
	c, err := Compile(raw)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate(json.RawMessage(`"` + secret + `"`))
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %v", core.CodeOf(err))
	}
	payload := err.Error()
	for _, v := range Violations(err) {
		if strings.Contains(v.Message, secret) {
			t.Fatalf("message leaks secret: %q", v.Message)
		}
		if strings.Contains(v.Location, secret) {
			t.Fatalf("location leaks secret: %q", v.Location)
		}
	}
	if strings.Contains(payload, secret) {
		t.Fatalf("error string leaks secret")
	}
}

func TestMultiViolationOrderDeterministic(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"b":{"type":"string","minLength":3},"a":{"type":"integer"},"d":{"type":"string","pattern":"^[a-z]+$","minLength":5}},"required":["c"]}`)
	c, err := Compile(raw)
	if err != nil {
		t.Fatal(err)
	}
	inst := json.RawMessage(`{"b":1,"a":"x","d":"A"}`)
	var first []Violation
	for range 3 {
		err = c.Validate(inst)
		vs := Violations(err)
		if first == nil {
			first = vs
			continue
		}
		if len(vs) != len(first) {
			t.Fatalf("count drift %+v vs %+v", first, vs)
		}
		for i := range vs {
			if vs[i] != first[i] {
				t.Fatalf("run differs: %+v vs %+v", first, vs)
			}
		}
	}
	want := []string{"/a", "/b", "/c", "/d", "/d"}
	if len(first) != len(want) {
		t.Fatalf("violations %+v", first)
	}
	for i, loc := range want {
		if first[i].Location != loc {
			t.Fatalf("index %d: %+v", i, first)
		}
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
