package expr

import "testing"

// Equal matches keys by lookup, not by position in the sorted entry slice:
// the ladder orders int before uint before double, so two maps with equal
// (but differently typed) keys can sort them into different positions.
func TestMixedKeyMapEqualityByLookup(t *testing.T) {
	if !evalBool(t, `{1: 'a', 2.0: 'b'} == {1u: 'a', 2: 'b'}`) {
		t.Fatal("equal mixed-key maps compared unequal")
	}
}

func TestMixedKeyMapEqualityDifferentValue(t *testing.T) {
	if evalBool(t, `{1: 'a', 2.0: 'b'} == {1u: 'a', 2: 'c'}`) {
		t.Fatal("maps with different values compared equal")
	}
}
