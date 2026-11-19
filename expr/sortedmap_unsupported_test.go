package expr

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
)

func TestUnsupportedMapKeyKindFailsClosed(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	src := `{duration('1s'): 1}.size()`
	p, err := env.Compile(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Eval(nil)
	if err == nil {
		t.Fatal("want error")
	}
	if core.CodeOf(err) != core.CodeInvalidArgument {
		t.Fatalf("code %s", core.CodeOf(err))
	}
	if !contains(err.Error(), "evaluation failed") {
		t.Fatalf("cause in %q", err.Error())
	}
}

func TestUnsupportedMapKeyOmitsSecretKey(t *testing.T) {
	secret := testSecret
	env, err := NewEnv([]Var{{Name: "noise", Schema: json.RawMessage(`{"type":"string"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`{duration('1s'): 1}.size()`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Eval(map[string]any{"noise": secret})
	assertErrorOmitsSecret(t, err, secret)
}
