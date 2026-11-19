package expr

import (
	"encoding/json"
	"testing"
)

func TestNestedObjectResultJSON(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{"outer":{"type":"object","properties":{"inner":{"type":"object","properties":{"n":{"type":"integer"}}}}}}
	}`)
	env, err := NewEnv([]Var{{Name: "state", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`state`, nil)
	if err != nil {
		t.Fatal(err)
	}
	bind := map[string]any{
		"state": map[string]any{
			"outer": map[string]any{
				"inner": map[string]any{"n": int64(7)},
			},
		},
	}
	res, err := p.Eval(bind)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"outer": map[string]any{
			"inner": map[string]any{"n": int64(7)},
		},
	}
	assertJSONEqual(t, res, want)
}

func TestListOfObjectsResultJSON(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{"items":{"type":"array","items":{"type":"object","properties":{"x":{"type":"integer"}}}}}
	}`)
	env, err := NewEnv([]Var{{Name: "state", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`state.items`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(map[string]any{"state": map[string]any{"items": []any{map[string]any{"x": int64(1)}, map[string]any{"x": int64(2)}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{map[string]any{"x": int64(1)}, map[string]any{"x": int64(2)}}
	assertJSONEqual(t, res, want)
}

func TestMapOfObjectsResultJSON(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{"byid":{"type":"object","additionalProperties":{"type":"object","properties":{"v":{"type":"integer"}}}}}
	}`)
	env, err := NewEnv([]Var{{Name: "state", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`state.byid`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(map[string]any{"state": map[string]any{"byid": map[string]any{"a": map[string]any{"v": int64(3)}}}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"a": map[string]any{"v": int64(3)}}
	assertJSONEqual(t, res, want)
}

func TestBoundNestedObjectResultJSON(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{"inner":{"type":"object","properties":{"a":{"type":"integer"}}}}
	}`)
	env, err := NewEnv([]Var{{Name: "o", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`o`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(map[string]any{"o": map[string]any{"inner": map[string]any{"a": int64(1)}}})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"inner": map[string]any{"a": int64(1)}}
	assertJSONEqual(t, res, want)
}

func assertJSONEqual(t *testing.T, got, want any) {
	t.Helper()
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Fatalf("got %s want %s", gj, wj)
	}
}
