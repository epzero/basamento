package expr

import (
	"testing"
)

func TestMixedMapKeyOrder(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	src := `{true: 1, 42: 2, 3u: 3, 'z': 4, 'a': 5}.map(k, k)`
	p, err := env.Compile(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(nil)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []string{"true", "int", "int", "s:a", "s:z"}
	got, ok := res.([]any)
	if !ok {
		t.Fatalf("got %T", res)
	}
	if len(got) != len(wantKinds) {
		t.Fatalf("got %v", got)
	}
	for i, w := range wantKinds {
		if gotKind(got[i]) != w {
			t.Fatalf("index %d: got %v (%s) want %s (full %v)", i, got[i], gotKind(got[i]), w, got)
		}
	}
}

func gotKind(v any) string {
	switch t := v.(type) {
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int64:
		return "int"
	case uint64:
		return "uint"
	case string:
		return "s:" + t
	default:
		return "?"
	}
}
