package expr

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCostLimitStopsAll(t *testing.T) {
	env, err := NewEnv([]Var{{Name: "items", Schema: json.RawMessage(`{"type":"array","items":{"type":"integer"}}`)}}, WithCostLimit(100))
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`items.all(i, i > 0)`, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := make([]any, 1000)
	for i := range items {
		items[i] = int64(i + 1)
	}
	_, err = p.Eval(map[string]any{"items": items})
	if err == nil {
		t.Fatal("want cost error")
	}
	if !strings.Contains(err.Error(), "cost limit") {
		t.Fatalf("want cost limit in error, got %v", err)
	}
}

func TestMapLiteralKeyOrder(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	src := `{'delta': 4, 'alpha': 1, 'charlie': 3, 'bravo': 2}.filter(k, k != 'charlie')`
	p, err := env.Compile(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []any{"alpha", "bravo", "delta"}
	got, ok := res.([]any)
	if !ok {
		t.Fatalf("got %T", res)
	}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}
