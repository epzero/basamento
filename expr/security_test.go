package expr

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEvalErrorOmitsSecretBinding(t *testing.T) {
	secret := "super-secret-token-xyzzy"
	env, err := NewEnv([]Var{{Name: "n", Schema: json.RawMessage(`{"type":"integer"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`10 / n`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Eval(map[string]any{"n": int64(0)})
	if err == nil {
		t.Fatal("want error")
	}
	msg := err.Error()
	if strings.Contains(msg, secret) {
		t.Fatalf("error leaked secret: %q", msg)
	}
	// Ensure a binding value does not appear when evaluation fails on another variable.
	env2, err := NewEnv([]Var{
		{Name: "items", Schema: json.RawMessage(`{"type":"array","items":{"type":"integer"}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := env2.Compile(`items[99]`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p2.Eval(map[string]any{"items": []any{int64(1)}})
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "super-secret") {
		t.Fatalf("error leaked data: %q", err.Error())
	}
}

func TestMappingErrorOmitsSecretInput(t *testing.T) {
	secret := "mapping-secret-abcdefghij"
	m, err := CompileMapping(`1 / 0`, nil)
	if err != nil {
		t.Fatal(err)
	}
	in := map[string]any{"payload": secret}
	_, err = m.Eval(in, nil)
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked secret: %q", err.Error())
	}
}
