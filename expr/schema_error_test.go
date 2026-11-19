package expr

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestNewEnvInvalidSchemaInvalidArgument(t *testing.T) {
	_, err := NewEnv([]Var{{Name: "x", Schema: json.RawMessage(`not json`)}})
	if err == nil {
		t.Fatal("want error")
	}
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %s", core.CodeOf(err))
	}
}

func TestNewEnvExternalRefInvalidArgument(t *testing.T) {
	_, err := NewEnv([]Var{{Name: "x", Schema: json.RawMessage(`{"$ref":"http://example.com/foo"}`)}})
	if err == nil {
		t.Fatal("want error")
	}
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %s", core.CodeOf(err))
	}
}

func TestSanitizeTypeNameNoCollision(t *testing.T) {
	a := sanitizeTypeName("state.a-b")
	b := sanitizeTypeName("state.a_b")
	if a == b {
		t.Fatalf("collision %q", a)
	}
}
