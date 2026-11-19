package expr

import (
	"testing"

	"cel.dev/cel-go/common/types"
)

// NativeToValue sorts every Go map, including maps whose key type is not
// string; it must not fall back to the unsorted inner adapter.
func TestNativeIntKeyMapWrappedAsSortedAnyMap(t *testing.T) {
	a := newSortedAdapter(nil)
	v := a.NativeToValue(map[int]string{2: "b", 1: "a"})
	m, ok := v.(*sortedAnyMap)
	if !ok {
		t.Fatalf("got %T want *sortedAnyMap", v)
	}
	it := m.Iterator()
	var keys []int64
	for it.HasNext() == types.True {
		keys = append(keys, int64(it.Next().(types.Int)))
	}
	if len(keys) != 2 || keys[0] != 1 || keys[1] != 2 {
		t.Fatalf("keys %v want [1 2]", keys)
	}
	if m.Get(types.Int(1)) != types.String("a") {
		t.Fatalf("value %v want a", m.Get(types.Int(1)))
	}
}

// String-keyed typed maps keep the sortedStringMap representation, so their
// results stay JSON values.
func TestNativeStringKeyTypedMapStaysSortedStringMap(t *testing.T) {
	a := newSortedAdapter(nil)
	v := a.NativeToValue(map[string]int{"z": 1, "a": 2})
	if _, ok := v.(*sortedStringMap); !ok {
		t.Fatalf("got %T want *sortedStringMap", v)
	}
}
