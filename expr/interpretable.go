package expr

import (
	"reflect"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
	"cel.dev/cel-go/interpreter"
)

func sortedMapDecorator(adapter *sortedAdapter) interpreter.InterpretableDecoratorV2 {
	return func(i interpreter.InterpretableV2) (interpreter.InterpretableV2, error) {
		if reflect.TypeOf(i).String() != "*interpreter.evalMap" {
			return i, nil
		}
		return &sortedEvalMap{InterpretableV2: i, adapter: adapter}, nil
	}
}

type sortedEvalMap struct {
	interpreter.InterpretableV2
	adapter *sortedAdapter
}

func (s *sortedEvalMap) Exec(frame *interpreter.ExecutionFrame) ref.Val {
	out := s.InterpretableV2.Exec(frame)
	return wrapSortedMapper(out, s.adapter)
}

// wrapSortedMapper gives a map literal's result a sorted representation. The
// entries keep their ref.Val values: only the result conversion
// (celValToJSONWalk) turns a duration, timestamp, bytes or uint64 into its
// JSON form, so reading a field still returns the CEL value.
func wrapSortedMapper(v ref.Val, adapter *sortedAdapter) ref.Val {
	m, ok := v.(traits.Mapper)
	if !ok || adapter == nil {
		return v
	}
	switch v.(type) {
	case *sortedStringMap, *sortedAnyMap:
		return v
	}
	entries, err := extractMapperEntries(m, adapter)
	if err != nil {
		return types.NewErr("unsupported map key type")
	}
	if len(entries) == 0 {
		// Empty map literal; unclassified key kinds fail in extractMapperEntries above.
		return newSortedAnyMap(adapter, nil)
	}
	allString := true
	for _, e := range entries {
		if e.key.kind != keyString {
			allString = false
			break
		}
	}
	if allString {
		data := make(map[string]any, len(entries))
		for _, e := range entries {
			data[e.key.s] = e.val
		}
		return &sortedStringMap{adapter: adapter, data: data}
	}
	return newSortedAnyMap(adapter, entries)
}
