package schema

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestCompileRelativeRefRejected(t *testing.T) {
	_, err := Compile(json.RawMessage(`{"$ref": "review.json"}`))
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %v", core.CodeOf(err))
	}
	if ReferenceFromErr(err) != "review.json" {
		t.Fatalf("ref %q", ReferenceFromErr(err))
	}
}

func TestCompileClosedWorldRefDeterministic(t *testing.T) {
	set := map[string]json.RawMessage{
		"https://example.com/a": json.RawMessage(`{"$ref":"https://example.com/missing-a"}`),
		"https://example.com/b": json.RawMessage(`{"$ref":"https://example.com/missing-b"}`),
	}
	var first string
	for range 20 {
		_, err := compileSet(set, "https://example.com/a")
		if core.CodeOf(err) != core.CodeInvalidArgument {
			t.Fatalf("code %v", core.CodeOf(err))
		}
		ref := ReferenceFromErr(err)
		if first == "" {
			first = ref
			continue
		}
		if ref != first {
			t.Fatalf("nondeterministic ref %q vs %q", first, ref)
		}
	}
}

func TestCompileInvalidSchemaLocation(t *testing.T) {
	_, err := Compile(json.RawMessage(`{"type": "object", "required": "approved"}`))
	vs := SchemaViolations(err)
	if len(vs) == 0 {
		t.Fatal(err)
	}
	found := false
	for _, v := range vs {
		if v.SchemaLocation == "/required" {
			found = true
		}
	}
	if !found {
		t.Fatalf("violations %+v", vs)
	}
}
