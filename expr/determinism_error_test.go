package expr

import "testing"

func TestMapComprehensionReportsLowestKeyError(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Sorted keys: a before b; key a divides by zero, key b fails double('x') if reached first.
	const lit = `{'b': 'x', 'a': 0}`
	src := lit + `.map(k, double(` + lit + `[k]) / 0.0)`
	p, err := env.Compile(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	const n = 100
	var first string
	for i := 0; i < n; i++ {
		_, err := p.Eval(nil)
		if err == nil {
			t.Fatal("want error")
		}
		msg := err.Error()
		if first == "" {
			first = msg
		} else if msg != first {
			t.Fatalf("iter %d: %q != %q", i, msg, first)
		}
	}
	if first == "" {
		t.Fatal("no error captured")
	}
}
