package expr

import (
	"fmt"
	"reflect"
	"unsafe"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/interpreter"
)

// guardFoldRange makes a comprehension over a map with a key of no order fail
// before it iterates the map (REQ-EXPR-022). cel-go's comprehension folder has
// no error path: a key it cannot order either drops the entry or surfaces a
// generic interruption. The range expression therefore returns the error
// itself, which the fold's own early error check propagates unchanged.
//
// The range is the fold's iterRange field, which cel-go does not export; it is
// wrapped in place so the fold still evaluates it once. If a cel-go upgrade
// changes that layout, building the program fails here instead of iterating an
// unordered map in Go order.
func guardFoldRange(i interpreter.InterpretableV2) (interpreter.InterpretableV2, error) {
	v := reflect.ValueOf(i)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return nil, fmt.Errorf("cel-go comprehension is not a pointer")
	}
	f := v.Elem().FieldByName("iterRange")
	if !f.IsValid() || f.Kind() != reflect.Interface || !f.CanAddr() {
		return nil, fmt.Errorf("cel-go comprehension has no iterRange")
	}
	slot := reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
	rangeNode, ok := slot.Interface().(interpreter.InterpretableV2)
	if !ok || rangeNode == nil {
		return nil, fmt.Errorf("cel-go comprehension range is not interpretable")
	}
	if _, done := rangeNode.(unorderedRangeGuard); done {
		return i, nil
	}
	slot.Set(reflect.ValueOf(unorderedRangeGuard{InterpretableV2: rangeNode}))
	return i, nil
}

// unorderedRangeGuard fails evaluation when its range is a map holding a key
// of no order.
type unorderedRangeGuard struct {
	interpreter.InterpretableV2
}

func (g unorderedRangeGuard) Exec(frame *interpreter.ExecutionFrame) ref.Val {
	v := g.InterpretableV2.Exec(frame)
	if m, ok := v.(*sortedAnyMap); ok && m.hasUnordered() {
		return types.NewErr("unordered key")
	}
	return v
}
