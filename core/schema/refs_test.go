package schema

import (
	"encoding/json"
	"testing"
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
			"type": "string"
		}`),
	}
	_, err := compileSet(set, "https://example.com/a.json")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
}