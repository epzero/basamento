package expr

import (
	"testing"

	"cel.dev/cel-go/common/types"
)

func TestNativeMapWrappedAsSortedStringMap(t *testing.T) {
	reg, err := types.NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	a := newSortedAdapter(reg)
	v := a.NativeToValue(map[string]any{"z": 1, "a": 2})
	if _, ok := v.(*sortedStringMap); !ok {
		t.Fatalf("got %T", v)
	}
}
