package expr

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBindVarsLowestNameReported(t *testing.T) {
	schema := json.RawMessage(`{"type":"integer"}`)
	env, err := NewEnv([]Var{
		{Name: "aa", Schema: schema},
		{Name: "zz", Schema: schema},
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`1`, nil)
	if err != nil {
		t.Fatal(err)
	}
	const n = 100
	var first string
	for i := 0; i < n; i++ {
		var vars map[string]any
		if i%2 == 0 {
			vars = map[string]any{"aa": "x", "zz": "y"}
		} else {
			vars = map[string]any{"zz": "y", "aa": "x"}
		}
		_, err := p.Eval(vars)
		if err == nil {
			t.Fatal("want bind error")
		}
		msg := err.Error()
		if !strings.Contains(msg, `variable "aa"`) {
			t.Fatalf("iter %d: %q missing aa", i, msg)
		}
		if first == "" {
			first = msg
		} else if msg != first {
			t.Fatalf("iter %d: %q != %q", i, msg, first)
		}
	}
}

func TestBuildObjectSchemaLowestPropertyReported(t *testing.T) {
	schema := json.RawMessage(`{
		"type":"object",
		"properties":{
			"zz":{"$ref":"http://example.com/zz"},
			"aa":{"$ref":"http://example.com/aa"}
		}
	}`)
	const n = 100
	var first string
	for i := 0; i < n; i++ {
		_, err := NewEnv([]Var{{Name: "state", Schema: schema}})
		if err == nil {
			t.Fatal("want schema error")
		}
		msg := err.Error()
		if !strings.Contains(msg, ".aa") {
			t.Fatalf("iter %d: %q missing .aa", i, msg)
		}
		if first == "" {
			first = msg
		} else if msg != first {
			t.Fatalf("iter %d: %q != %q", i, msg, first)
		}
	}
}
