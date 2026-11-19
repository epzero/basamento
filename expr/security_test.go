package expr

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/epzero/basamento/core"
)

const testSecret = "super-secret-token-xyzzy-42"

func assertErrorOmitsSecret(t *testing.T, err error, secret string) {
	t.Helper()
	if err == nil {
		t.Fatal("want error")
	}
	forEachErrorInChain(err, func(e error) {
		if strings.Contains(e.Error(), secret) {
			t.Fatalf("error chain leaked %q: %s", secret, e.Error())
		}
		var pe *core.Error
		if errors.As(e, &pe) && pe.Details != nil {
			d, _ := json.Marshal(pe.Details)
			if strings.Contains(string(d), secret) {
				t.Fatalf("Details leaked %q: %s", secret, d)
			}
		}
	})
}

func forEachErrorInChain(err error, fn func(error)) {
	type multiUnwrap interface {
		Unwrap() []error
	}
	seen := map[error]struct{}{}
	var walk func(error)
	walk = func(e error) {
		if e == nil {
			return
		}
		if _, ok := seen[e]; ok {
			return
		}
		seen[e] = struct{}{}
		fn(e)
		if mu, ok := e.(multiUnwrap); ok {
			for _, sub := range mu.Unwrap() {
				walk(sub)
			}
			return
		}
		walk(errors.Unwrap(e))
	}
	walk(err)
}

func TestMapCauseStripsOperandText(t *testing.T) {
	raw := fmt.Sprintf("no matching overload for wrong_type_%s", testSecret)
	if mapCELCause(raw) == raw {
		t.Fatal("mapCELCause passthrough would leak operands")
	}
	raw2 := fmt.Sprintf("jq: %s division", testSecret)
	if mapGoJQCause(raw2) == raw2 {
		t.Fatal("mapGoJQCause passthrough would leak operands")
	}
}

func TestEvalErrorOmitsSecretBinding(t *testing.T) {
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
	assertErrorOmitsSecret(t, err, secret)

	env3, err := NewEnv([]Var{
		{Name: "items", Schema: json.RawMessage(`{"type":"array","items":{"type":"integer"}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	p3, err := env3.Compile(`items[0] + 1`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p3.Eval(map[string]any{"items": []any{secret}})
	assertErrorOmitsSecret(t, err, secret)
}

func TestCompileErrorOmitsSecretInExpressionSource(t *testing.T) {
	secret := testSecret
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = env.Compile(secret+` + 1`, nil)
	if err == nil {
		t.Fatal("want compile error")
	}
	// Source is named in compile errors by design; ensure details do not repeat operand bindings.
	var pe *core.Error
	if errors.As(err, &pe) && pe.Details != nil {
		d, _ := json.Marshal(pe.Details)
		if strings.Contains(string(d), secret) && !strings.Contains(err.Error(), secret) {
			t.Fatalf("unexpected details payload: %s", d)
		}
	}
}

func TestMappingErrorOmitsSecretInput(t *testing.T) {
	secret := testSecret
	m, err := CompileMapping(`.payload + 0`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Eval(map[string]any{"payload": secret}, nil)
	assertErrorOmitsSecret(t, err, secret)

	m2, err := CompileMapping(`.items[0] + 1`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m2.Eval(map[string]any{"items": []any{secret}}, nil)
	assertErrorOmitsSecret(t, err, secret)

	m3, err := CompileMapping(`$secret + 0`, []string{"$secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m3.Eval(nil, map[string]any{"$secret": secret})
	assertErrorOmitsSecret(t, err, secret)
}

func TestBindErrorOmitsSecretValue(t *testing.T) {
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
	assertErrorOmitsSecret(t, err, secret)
}
