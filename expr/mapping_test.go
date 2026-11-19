package expr

import (
	"encoding/json"
	"testing"
)

func TestMappingParams(t *testing.T) {
	m, err := CompileMapping(`{prompt: .text, n: ($params.count // 1)}`, []string{"$params"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Eval(map[string]any{"text": "a red kite"}, map[string]any{"$params": map[string]any{"count": 2}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"prompt": "a red kite", "n": float64(2)}
	got, _ := json.Marshal(out)
	wh, _ := json.Marshal(want)
	if string(got) != string(wh) {
		t.Fatalf("got %s want %s", got, wh)
	}
}
