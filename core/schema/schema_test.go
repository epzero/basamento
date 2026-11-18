package schema

import (
	"encoding/json"
	"testing"
)

func TestSetAddZeroValue(t *testing.T) {
	var s Set
	if err := s.Add("https://example.com/x", json.RawMessage(`{"type":"string"}`)); err != nil {
		t.Fatal(err)
	}
}
