package expr

import (
	"encoding/json"
	"testing"

	"github.com/epzero/basamento/core"
)

// A key of no order keeps its entry: the map builds and its size counts it
// (REQ-EXPR-022). Iteration is what fails.
func TestUnorderedMapKeyBuildsAndCounts(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	src := `{duration('1s'): 1}.size()`
	p, err := env.Compile(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(nil)
	if err != nil {
		t.Fatalf("evaluation failed: %v", err)
	}
	if res != int64(1) {
		t.Fatalf("size %v want 1", res)
	}
}

func TestUnorderedMapKeyIterationFails(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	src := `{duration('1s'): 1}.map(k, k)`
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
	if !contains(err.Error(), "unordered key") {
		t.Fatalf("cause in %q", err.Error())
	}
}

func TestUnorderedMapKeyOmitsSecretKey(t *testing.T) {
	secret := testSecret
	env, err := NewEnv([]Var{{Name: "noise", Schema: json.RawMessage(`{"type": "string"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`{duration('1s'): 1}.map(k, k)`, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Eval(map[string]any{"noise": secret})
	if err == nil {
		t.Fatal("want error")
	}
	assertErrorOmitsSecret(t, err, secret)
}
