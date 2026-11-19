package expr

import (
	"encoding/json"
	"math"
	"strconv"
	"testing"

	"cel.dev/cel-go/common/types"
)

func TestUintResultExactJSON(t *testing.T) {
	u := types.Uint(uint64(math.MaxInt64) + 1)
	j, err := celValToJSON(u)
	if err != nil {
		t.Fatal(err)
	}
	n, ok := j.(json.Number)
	if !ok {
		t.Fatalf("got %T %v", j, j)
	}
	want := strconv.FormatUint(uint64(math.MaxInt64)+1, 10)
	if string(n) != want {
		t.Fatalf("got %s want %s", n, want)
	}
}

func TestIntKeyMapNoJSONForm(t *testing.T) {
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
		t.Fatal("want no JSON form error")
	}
}
