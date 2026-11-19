package expr

import "testing"

// TestMixedKeyMapEqualitySameKinds pins that identical mixed-key maps compare
// equal.
func TestMixedKeyMapEqualitySameKinds(t *testing.T) {
	if !evalBool(t, `{1: 'a', 2.0: 'b'} == {1: 'a', 2.0: 'b'}`) {
		t.Fatal("identical mixed-key maps compared unequal")
	}
}

// TestMapKeyEqualityAcrossNumericKinds pins REQ-EXPR-021 and the stock cel-go
// v0.33.4 result for the reviewer's table: an int, uint or double key and a key
// of another of those kinds with the same value are the same key.
func TestMapKeyEqualityAcrossNumericKinds(t *testing.T) {
	tests := []struct {
		expression string
		want       bool
	}{
		{`{1: 'a', 2.0: 'b'} == {1u: 'a', 2: 'b'}`, true},
		{`{1: 'a', 2.0: 'b'} == {1.0: 'a', 2: 'b'}`, true},
		{`{1: 'a', 2.0: 'b'} == {1: 'a', 2.0: 'c'}`, false},
		{`{1: 'a', 'x': 0} == {'1': 'a', 'x': 0}`, false},
		// Lossless only: a double that is not an integer, or a uint beyond
		// int64, is not the same key as any int or uint.
		{`{1: 'a', 2.0: 'b'} == {1.5: 'a', 2: 'b'}`, false},
		{`{1: 'a', 2.0: 'b'} == {9223372036854775808u: 'a', 2: 'b'}`, false},
		// Two left entries cannot both match one right entry (round 3).
		{`{1: 'a', 1u: 'a'} == {1: 'a', 2: 'a'}`, false},
		{`{1: 'a', 1.0: 'a'} == {1: 'a', 2: 'a'}`, false},
		{`{1: 'a', 1u: 'a'} == {1: 'a', 1u: 'a'}`, true},
	}
	for _, tc := range tests {
		if got := evalBool(t, tc.expression); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.expression, got, tc.want)
		}
	}
}

// TestMapKeyEqualityIsOperandOrderIndependent pins that == compares numeric
// keys by value in either operand order (REQ-EXPR-021): a double entry key
// matches an int or uint key exactly as the reverse does, so which side of ==
// a literal is on cannot change the result.
func TestMapKeyEqualityIsOperandOrderIndependent(t *testing.T) {
	tests := []struct {
		left, right string
		want        bool
	}{
		{`{1: 'a', 2.0: 'b'}`, `{1u: 'a', 2: 'b'}`, true},
		{`{1: 'a', 2.0: 'b'}`, `{1.0: 'a', 2: 'b'}`, true},
		{`{1u: 'a', 2.0: 'b'}`, `{1: 'a', 2: 'b'}`, true},
		{`{-1.0: 'a', 2: 'b'}`, `{-1: 'a', 2.0: 'b'}`, true},
		{`{9223372036854775808u: 'a', 2.0: 'b'}`, `{9223372036854775808.0: 'a', 2: 'b'}`, true},
		{`{1: 'a', 'x': 0}`, `{'1': 'a', 'x': 0}`, false},
	}
	for _, tc := range tests {
		if got := evalBool(t, tc.left+" == "+tc.right); got != tc.want {
			t.Fatalf("%s == %s: got %v want %v", tc.left, tc.right, got, tc.want)
		}
		if got := evalBool(t, tc.right+" == "+tc.left); got != tc.want {
			t.Fatalf("%s == %s: got %v want %v", tc.right, tc.left, got, tc.want)
		}
	}
}

// TestMapKeyContainsAcrossNumericKinds pins the membership and comprehension
// halves of REQ-EXPR-021: `in`, exists and exists_one find a numeric key of
// one kind with a key of another, and never a string key.
func TestMapKeyContainsAcrossNumericKinds(t *testing.T) {
	tests := []struct {
		expression string
		want       bool
	}{
		{`1u in {1: 'a', 2.0: 'b'}`, true},
		{`1.0 in {1: 'a', 2.0: 'b'}`, true},
		{`1 in {1.0: 'a', 2u: 'b'}`, true},
		{`1.0 in {1u: 'a', 2.0: 'b'}`, true},
		{`'1' in {1: 'a', 2.0: 'b'}`, false},
		{`{1: 'a', 2.0: 'b'}.exists(k, k == 1u)`, true},
		{`{1.0: 'a', 2u: 'b'}.exists_one(k, k == 1)`, true},
	}
	for _, tc := range tests {
		if got := evalBool(t, tc.expression); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.expression, got, tc.want)
		}
	}
}

// TestMapLookupAcrossNumericKinds pins the lookup half of REQ-EXPR-021 and of
// the reviewer's table.
func TestMapLookupAcrossNumericKinds(t *testing.T) {
	tests := []struct {
		expression string
		want       string
	}{
		{`{1: 'a', 2.0: 'b'}[1u]`, "a"},
		{`{1.0: 'a', 2: 'b'}[1]`, "a"},
		{`{1: 'a', 2.0: 'b'}[1.0]`, "a"},
		{`{1u: 'a', 2.0: 'b'}[2.0]`, "b"},
	}
	for _, tc := range tests {
		env, err := NewEnv(nil)
		if err != nil {
			t.Fatal(err)
		}
		p, err := env.Compile(tc.expression, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.expression, err)
		}
		res, err := p.Eval(nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.expression, err)
		}
		if res != tc.want {
			t.Fatalf("%s: got %v want %q", tc.expression, res, tc.want)
		}
	}
}

// TestMapLookupAcrossNumericKindsIsLossless pins that the cross-kind fallback
// never matches a numeric key that only approximates the looked-up value.
func TestMapLookupAcrossNumericKindsIsLossless(t *testing.T) {
	for _, expression := range []string{
		`{1: 'a', 2.0: 'b'}[1.5]`,
		`{1: 'a', 2.0: 'b'}[9223372036854775808u]`,
		`{1: 'a', 2.0: 'b'}[3]`,
	} {
		env, err := NewEnv(nil)
		if err != nil {
			t.Fatal(err)
		}
		p, err := env.Compile(expression, nil)
		if err != nil {
			t.Fatalf("%s: %v", expression, err)
		}
		if _, err := p.Eval(nil); err == nil {
			t.Fatalf("%s: found a key", expression)
		}
	}
}

// TestUnorderedKeyIdentityIsCELValue pins that a key of no order is matched by
// CEL equality, not by its display string: [1, 2] and ["1", "2"] render the
// same but are different keys, and neither is [1, "2"] (round 4).
func TestUnorderedKeyIdentityIsCELValue(t *testing.T) {
	if !evalBool(t, `[1, 2] in {[1, 2]: 'int', ["1", "2"]: 'string'}`) {
		t.Fatal("a list key was not found")
	}
	if !evalBool(t, `["1", "2"] in {[1, 2]: 'int', ["1", "2"]: 'string'}`) {
		t.Fatal("a string list key was not found")
	}
	if evalBool(t, `[1, "2"] in {[1, 2]: 'int', ["1", "2"]: 'string'}`) {
		t.Fatal("[1, \"2\"] matched only by its display string")
	}
}

// TestUnorderedKeyIdentityStable pins that the list-key answer does not depend
// on map iteration order.
func TestUnorderedKeyIdentityStable(t *testing.T) {
	env, err := NewEnv(nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := env.Compile(`[1, "2"] in {[1, 2]: 'first', ["1", "2"]: 'second'}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		res, err := p.Eval(nil)
		if err != nil {
			t.Fatal(err)
		}
		if res != false {
			t.Fatalf("iteration %d: %v", i, res)
		}
	}
}
