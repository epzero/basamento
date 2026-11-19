package expr

import (
	"encoding/json"
	"testing"
)

func TestObjectFieldSelectAndHas(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"reviewer":{"type":"string"},
			"nested":{"type":"object","properties":{"n":{"type":"integer"}}}
		}
	}`)
	env, err := NewEnv([]Var{{Name: "state", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		src  string
		bind map[string]any
		want any
	}{
		{`state.reviewer == 'ana'`, map[string]any{"state": map[string]any{"reviewer": "ana"}}, true},
		{`state.nested.n == 3`, map[string]any{"state": map[string]any{"nested": map[string]any{"n": int64(3)}}}, true},
		{`has(state.reviewer) ? state.reviewer : 'nobody'`, map[string]any{"state": map[string]any{}}, "nobody"},
		{`!has(state.reviewer)`, map[string]any{"state": map[string]any{}}, true},
	}
	for _, tc := range cases {
		p, err := env.Compile(tc.src, nil)
		if err != nil {
			t.Fatalf("%q compile: %v", tc.src, err)
		}
		got, err := p.Eval(tc.bind)
		if err != nil {
			t.Fatalf("%q eval: %v", tc.src, err)
		}
		if got != tc.want {
			t.Fatalf("%q got %v want %v", tc.src, got, tc.want)
		}
	}
}

func TestObjectListSelect(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{"items":{"type":"array","items":{"type":"object","properties":{"x":{"type":"integer"}}}}}
	}`)
	env, err := NewEnv([]Var{{Name: "state", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	src := `state.items[0].x == 1`
	p, err := env.Compile(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.Eval(map[string]any{"state": map[string]any{"items": []any{map[string]any{"x": int64(1)}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got != true {
		t.Fatalf("got %v", got)
	}
}
