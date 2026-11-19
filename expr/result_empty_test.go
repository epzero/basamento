package expr

import (
	"encoding/json"
	"testing"
)

func TestEmptyMapLiteralJSON(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`{}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(nil)
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := res.(map[string]any); !ok || len(m) != 0 {
		t.Fatalf("got %#v", res)
	}
}

func TestEmptyMapInListAndNested(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []string{
		`[{}]`,
		`{'a': {}}`,
		`[1, 2].map(x, {})`,
	} {
		p, err := env.Compile(src, nil)
		if err != nil {
			t.Fatalf("%q compile: %v", src, err)
		}
		if _, err := p.Eval(nil); err != nil {
			t.Fatalf("%q eval: %v", src, err)
		}
	}
}

func TestNonEmptyNonStringKeyMapStillNoJSON(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`{1: 'a'}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Eval(nil)
	if err == nil {
		t.Fatal("want REQ-EXPR-012 no JSON form")
	}
	if !contains(err.Error(), "no JSON form") {
		t.Fatalf("got %v", err)
	}
}

func TestMappingOutputEmptyObject(t *testing.T) {
	m, err := CompileMapping(`{}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Eval(map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	gj, _ := json.Marshal(out)
	if string(gj) != `{}` {
		t.Fatalf("got %s", gj)
	}
}
