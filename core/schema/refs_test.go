package schema

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestRefsClosedWorldFragmentSibling(t *testing.T) {
	set := map[string]json.RawMessage{
		"https://example.com/review.json": json.RawMessage(`{"type":"string"}`),
		"https://example.com/main.json": json.RawMessage(`{
			"type": "object",
			"properties": {"status": {"$ref": "review.json#/$defs/Status"}},
			"$defs": {"Status": {"type": "string"}}
		}`),
	}
	if err := validateRefsClosed("https://example.com/main.json", set["https://example.com/main.json"], set); err != nil {
		t.Fatalf("refs: %v", err)
	}
}

func TestRefsSkipsConstDefaultExamples(t *testing.T) {
	set := map[string]json.RawMessage{
		"https://example.com/a.json": json.RawMessage(`{
			"const": {"$ref": "missing.json"},
			"default": {"$ref": "missing.json"},
			"examples": [{"$ref": "missing.json"}],
			"enum": [{"$ref": "missing.json"}],
			"type": "string"
		}`),
	}
	_, err := compileSet(set, "https://example.com/a.json")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
}

// A member named like a data keyword under properties/$defs holds a schema,
// not instance data, so its $ref is resolved normally.
func TestRefsWalkSubschemaNamedLikeDataKeyword(t *testing.T) {
	set := map[string]json.RawMessage{
		"https://example.com/defs.json": json.RawMessage(`{"$defs":{"Status":{"type":"string"}}}`),
	}
	raw := json.RawMessage(`{
		"type": "object",
		"properties": {"default": {"$ref": "missing.json"}, "enum": {"type": "string"}},
		"$defs": {"const": {"$ref": "missing.json"}}
	}`)
	err := validateRefsClosed("https://example.com/b.json", raw, set)
	if err == nil || core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("want invalid_argument for missing ref, got %v", err)
	}
}

func TestRefsSkipDataKeywordOnlyAtSchemaLevel(t *testing.T) {
	set := map[string]json.RawMessage{
		"https://example.com/c.json": json.RawMessage(`{
			"type": "object",
			"const": {"$ref": "missing.json"},
			"properties": {"status": {"type": "string"}},
			"$defs": {"status": {"$ref": "https://example.com/defs.json#/$defs/Status"}}
		}`),
		"https://example.com/defs.json": json.RawMessage(`{"$defs":{"Status":{"type":"string"}}}`),
	}
	if _, err := compileSet(set, "https://example.com/c.json"); err != nil {
		t.Fatalf("compile: %v", err)
	}
}
