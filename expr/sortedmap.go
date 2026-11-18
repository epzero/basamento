package expr

import (
	"fmt"
	"reflect"
	"sort"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
)

type sortedAdapter struct {
	inner ref.TypeAdapter
}

func newSortedAdapter(inner ref.TypeAdapter) *sortedAdapter {
	if inner == nil {
		inner = types.DefaultTypeAdapter
	}
	return &sortedAdapter{inner: inner}
}

func (a *sortedAdapter) NativeToValue(value any) ref.Val {
	switch v := value.(type) {
	case map[string]any:
		return &sortedStringMap{adapter: a, data: v}
	case map[any]any:
		return a.inner.NativeToValue(v)
	default:
		rv := reflect.ValueOf(value)
		if rv.Kind() == reflect.Map && rv.Type().Key().Kind() == reflect.String {
			out := make(map[string]any, rv.Len())
			iter := rv.MapRange()
			for iter.Next() {
				out[iter.Key().String()] = iter.Value().Interface()
			}
			return &sortedStringMap{adapter: a, data: out}
		}
		return a.inner.NativeToValue(value)
	}
}

type sortedStringMap struct {
	adapter *sortedAdapter
	data    map[string]any
}

func (m *sortedStringMap) ConvertToNative(typeDesc reflect.Type) (any, error) {
	return m.data, nil
}

func (m *sortedStringMap) ConvertToType(typeVal ref.Type) ref.Val {
	return m.adapter.inner.NativeToValue(m.data).ConvertToType(typeVal)
}

func (m *sortedStringMap) Equal(other ref.Val) ref.Val {
	return m.adapter.inner.NativeToValue(m.data).Equal(other)
}

func (m *sortedStringMap) Type() ref.Type {
	return types.NewMapType(types.StringType, types.DynType)
}

func (m *sortedStringMap) Value() any { return m.data }

func (m *sortedStringMap) Contains(key ref.Val) ref.Val {
	ks, ok := key.(types.String)
	if !ok {
		return types.Bool(false)
	}
	_, ok = m.data[string(ks)]
	return types.Bool(ok)
}

func (m *sortedStringMap) Find(key ref.Val) (ref.Val, bool) {
	ks, ok := key.(types.String)
	if !ok {
		return types.NewErr("no such key"), false
	}
	v, ok := m.data[string(ks)]
	if !ok {
		return types.NewErr("no such key"), false
	}
	return m.adapter.NativeToValue(v), true
}

func (m *sortedStringMap) Get(key ref.Val) ref.Val {
	v, ok := m.Find(key)
	if !ok {
		return types.NewErr("no such key")
	}
	return v
}

func (m *sortedStringMap) Iterator() traits.Iterator {
	keys := make([]string, 0, len(m.data))
	for k := range m.data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return &sortedMapIter{keys: keys, idx: -1}
}

func (m *sortedStringMap) Size() ref.Val {
	return types.Int(len(m.data))
}

type sortedMapIter struct {
	keys []string
	idx  int
}

func (it *sortedMapIter) ConvertToNative(typeDesc reflect.Type) (any, error) {
	return nil, fmt.Errorf("type conversion on iterators not supported")
}

func (it *sortedMapIter) ConvertToType(typeVal ref.Type) ref.Val {
	return types.NewErr("no such overload")
}

func (it *sortedMapIter) Equal(other ref.Val) ref.Val {
	return types.NewErr("no such overload")
}

func (it *sortedMapIter) Type() ref.Type {
	return types.IteratorType
}

func (it *sortedMapIter) Value() any {
	return nil
}

func (it *sortedMapIter) HasNext() ref.Val {
	return types.Bool(it.idx+1 < len(it.keys))
}

func (it *sortedMapIter) Next() ref.Val {
	if it.idx+1 >= len(it.keys) {
		return types.NewErr("no such key")
	}
	it.idx++
	return types.String(it.keys[it.idx])
}
