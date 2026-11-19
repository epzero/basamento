package expr

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestEvalErrorDetailsOmitBindings(t *testing.T) {
	secret := testSecret
	env, err := NewEnv([]Var{{Name: "n", Schema: json.RawMessage(`{"type":"integer"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`n + 1`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Eval(map[string]any{"n": secret})
	if err == nil {
		t.Fatal("want error")
	}
	var pe *core.Error
	if !errors.As(err, &pe) {
		t.Fatalf("got %T", err)
	}
	if pe.Details != nil {
		d, _ := json.Marshal(pe.Details)
		if strings.Contains(string(d), secret) {
			t.Fatalf("details must not echo bindings: %s", d)
		}
	}
}

func TestMappingEvalErrorDetailsEmpty(t *testing.T) {
	secret := testSecret
	m, err := CompileMapping(`.payload + 0`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Eval(map[string]any{"payload": secret}, nil)
	if err == nil {
		t.Fatal("want error")
	}
	var pe *core.Error
	if !errors.As(err, &pe) {
		t.Fatalf("got %T", err)
	}
	if pe.Details != nil {
		d, _ := json.Marshal(pe.Details)
		if strings.Contains(string(d), secret) {
			t.Fatalf("details leaked: %s", d)
		}
	}
}
