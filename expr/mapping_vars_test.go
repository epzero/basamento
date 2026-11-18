package expr

import (
	"encoding/json"
	"testing"
)

func TestMappingEvalNormalizesVars(t *testing.T) {
	m, err := CompileMapping(`$n`, []string{"$n"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Eval(nil, map[string]any{"$n": json.Number("42")})
	if err != nil {
		t.Fatal(err)
	}
	if out != float64(42) {
		t.Fatalf("got %v (%T)", out, out)
	}
}
