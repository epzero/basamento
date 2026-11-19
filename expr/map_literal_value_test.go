package expr

import "testing"

func evalBool(t *testing.T, src string) bool {
	t.Helper()
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(src, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := p.Eval(nil)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	b, ok := res.(bool)
	if !ok {
		t.Fatalf("%s: got %v (%T) want bool", src, res, res)
	}
	return b
}

// A map literal keeps CEL values until the result is converted: reading a
// field returns the duration itself, not its JSON form.
func TestMapLiteralDurationFieldKeepsCELValue(t *testing.T) {
	if !evalBool(t, `{'d': duration('1s')}.d == duration('1s')`) {
		t.Fatal("duration field lost its CEL value")
	}
}

// An infinite double is readable from a map literal; only converting the
// result to JSON fails (see TestMapLiteralInfNoJSONForm).
func TestMapLiteralInfFieldKeepsCELValue(t *testing.T) {
	if !evalBool(t, `{'x': 1.0/0.0}.x > 0.0`) {
		t.Fatal("inf field lost its CEL value")
	}
}

func TestNestedMapLiteralKeepsCELValue(t *testing.T) {
	if !evalBool(t, `{'a': {'b': duration('1s')}}.a.b == duration('1s')`) {
		t.Fatal("nested duration field lost its CEL value")
	}
}

func TestMapLiteralDurationEquality(t *testing.T) {
	if !evalBool(t, `{'d': duration('1s')} == {'d': duration('1s')}`) {
		t.Fatal("map literals holding equal durations compared unequal")
	}
}
