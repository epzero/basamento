package expr

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestMappingRepeatProducesTwo(t *testing.T) {
	m, err := CompileMapping(`repeat(1)`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Eval(nil, nil)
	if err == nil {
		t.Fatal("want error")
	}
	if got := err.Error(); !contains(got, "more than one value") {
		t.Fatalf("got %q", got)
	}
}

func TestMappingBigIntID(t *testing.T) {
	m, err := CompileMapping(`.id`, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"id": 12345678901234567890}`
	var in any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&in); err != nil {
		t.Fatal(err)
	}
	out, err := m.Eval(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := json.Number("12345678901234567890")
	if fmt.Sprint(out) != fmt.Sprint(want) {
		t.Fatalf("got %v (%T) want %v", out, out, want)
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
