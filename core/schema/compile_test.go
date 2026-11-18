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
