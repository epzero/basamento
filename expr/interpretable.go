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

func wrapSortedMapper(v ref.Val, adapter *sortedAdapter) ref.Val {
	m, ok := v.(traits.Mapper)
	if !ok || adapter == nil {
		return v
	}
	switch v.(type) {
	case *sortedStringMap, *sortedAnyMap:
		return v
	}
	entries := extractMapperEntries(m, adapter)
	if len(entries) == 0 {
		// Fall back to string-only extraction for string-keyed maps.
		data := map[string]any{}
		it := m.Iterator()
		for it.HasNext().Equal(types.True) == types.True {
			k := it.Next()
			ks, ok := k.(types.String)
			if !ok {
				return v
			}
			val, found := m.Find(k)
			if !found {
				continue
			}
			nat, err := val.ConvertToNative(reflect.TypeFor[any]())
			if err != nil {
				return v
			}
			data[string(ks)] = nat
		}
		if len(data) == 0 {
			return v
		}
		return &sortedStringMap{adapter: adapter, data: data}
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
			nat, err := e.val.ConvertToNative(reflect.TypeFor[any]())
			if err != nil {
				return v
			}
			data[e.key.s] = nat
		}
		return &sortedStringMap{adapter: adapter, data: data}
	}
	return newSortedAnyMap(adapter, entries)
}
