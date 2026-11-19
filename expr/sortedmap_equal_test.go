package expr

import "testing"

// Map keys of different kinds are separate entries: an int key and a double
// key never compare equal (REQ-EXPR-021), so the same value under different
// kinds makes two maps different.
func TestMixedKeyMapEqualitySameKinds(t *testing.T) {
	if !evalBool(t, `{1: 'a', 2.0: 'b'} == {1: 'a', 2.0: 'b'}`) {
		t.Fatal("identical mixed-key maps compared unequal")
	}
}

func TestMixedKeyMapEqualityDifferentKinds(t *testing.T) {
	if evalBool(t, `{1: 'a', 2.0: 'b'} == {1u: 'a', 2: 'b'}`) {
		t.Fatal("maps with keys of different kinds compared equal")
	}
}

func TestMixedKeyMapEqualityDifferentValue(t *testing.T) {
	if evalBool(t, `{1: 'a', 2.0: 'b'} == {1: 'a', 2.0: 'c'}`) {
		t.Fatal("maps with different values compared equal")
	}
}
