package expr

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/epzero/basamento/core"
)

func TestCycleBindRejected(t *testing.T) {
	m := map[string]any{"n": int64(1)}
	m["self"] = m
	env, err := NewEnv([]Var{{Name: "x", Schema: json.RawMessage(`{"type":"object"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`x`, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = p.Eval(map[string]any{"x": m})
	if err == nil {
		t.Fatal("want depth error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("cycle bind too slow")
	}
	if code := core.CodeOf(err); code != core.CodeInvalidArgument {
		t.Fatalf("code %s", code)
	}
}

func TestDeepBindRejected(t *testing.T) {
	m := map[string]any{}
	cur := m
	for i := 0; i < maxJSONWalkDepth+2; i++ {
		next := map[string]any{}
		cur["child"] = next
		cur = next
	}
	env, err := NewEnv([]Var{{Name: "x", Schema: json.RawMessage(`{"type":"object"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`x`, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = p.Eval(map[string]any{"x": m})
	if err == nil {
		t.Fatal("want error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("deep bind too slow")
	}
}

func TestDeepMappingInputRejected(t *testing.T) {
	m := map[string]any{}
	cur := m
	for i := 0; i < maxJSONWalkDepth+2; i++ {
		next := map[string]any{"v": 1}
		cur["n"] = next
		cur = next
	}
	mapp, err := CompileMapping(`.`, nil)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = mapp.Eval(m, nil)
	if err == nil {
		t.Fatal("want error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("deep mapping input too slow")
	}
}

func TestDeepEvalResultRejected(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{"child":{"type":"object","properties":{"child":{"type":"object"}}}}
	}`)
	env, err := NewEnv([]Var{{Name: "x", Schema: schema}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`x`, nil)
	if err != nil {
		t.Fatal(err)
	}
	bind := map[string]any{}
	cur := bind
	for i := 0; i < maxJSONWalkDepth+2; i++ {
		node := map[string]any{}
		cur["child"] = node
		cur = node
	}
	start := time.Now()
	_, err = p.Eval(map[string]any{"x": bind})
	if err == nil {
		t.Fatal("want error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("deep result too slow")
	}
	if !strings.Contains(err.Error(), "evaluation failed") {
		t.Fatalf("got %v", err)
	}
}
