package expr

import (
	"fmt"
	"math"
	"reflect"
	"sort"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
)

type sortedAdapter struct {
	inner ref.TypeAdapter //nolint:staticcheck // cel-go still exposes ref.TypeAdapter on the registry.
}

func newSortedAdapter(inner ref.TypeAdapter) *sortedAdapter { //nolint:staticcheck
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
		entries, err := nativeAnyMapEntries(a, v)
		if err != nil {
			return types.NewErrFromString(err.Error())
		}
		return newSortedAnyMap(a, entries)
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

type mapKeyKind int

const (
	keyBool mapKeyKind = iota
	keyInt
	keyUint
	keyDouble
	keyString
)

type mapKey struct {
	kind mapKeyKind
	b    bool
	i    int64
	u    uint64
	d    float64
	s    string
	key  ref.Val
}

func classifyMapKey(k ref.Val) (mapKey, bool) {
	switch v := k.(type) {
	case types.Bool:
		return mapKey{kind: keyBool, b: bool(v), key: k}, true
	case types.Int:
		return mapKey{kind: keyInt, i: int64(v), key: k}, true
	case types.Uint:
		return mapKey{kind: keyUint, u: uint64(v), key: k}, true
	case types.Double:
		f := float64(v)
		return mapKey{kind: keyDouble, d: f, key: k}, true
	case types.String:
		return mapKey{kind: keyString, s: string(v), key: k}, true
	default:
		return mapKey{}, false
	}
}

// mapKeyDoubleLess orders bool < int < uint < double < string at the map level.
// For doubles it orders values ascending numerically, places −0 before +0, and
// orders all NaNs last (tie-broken by math.Float64bits).
func mapKeyDoubleLess(a, b float64) bool {
	aNaN, bNaN := math.IsNaN(a), math.IsNaN(b)
	if aNaN && bNaN {
		return math.Float64bits(a) < math.Float64bits(b)
	}
	if aNaN {
		return false
	}
	if bNaN {
		return true
	}
	if a == 0 && b == 0 {
		return math.Signbit(a) && !math.Signbit(b)
	}
	return a < b
}

func (k mapKey) less(o mapKey) bool {
	if k.kind != o.kind {
		return k.kind < o.kind
	}
	switch k.kind {
	case keyBool:
		return !k.b && o.b
	case keyInt:
		return k.i < o.i
	case keyUint:
		return k.u < o.u
	case keyDouble:
		return mapKeyDoubleLess(k.d, o.d)
	case keyString:
		return k.s < o.s
	default:
		return false
	}
}

type sortedEntry struct {
	key mapKey
	val ref.Val
}

type sortedAnyMap struct {
	adapter *sortedAdapter
	entries []sortedEntry
}

func mapKeysEqual(a, b mapKey) bool {
	if a.kind == b.kind {
		switch a.kind {
		case keyBool:
			return a.b == b.b
		case keyInt:
			return a.i == b.i
		case keyUint:
			return a.u == b.u
		case keyDouble:
			return a.d == b.d
		case keyString:
			return a.s == b.s
		default:
			return false
		}
	}
	return mapKeysCrossEqual(a, b)
}

func mapKeysCrossEqual(a, b mapKey) bool {
	ai, aok := mapKeyAsInt64(a)
	bi, bok := mapKeyAsInt64(b)
	if aok && bok {
		return ai == bi
	}
	ad, adok := mapKeyAsDouble(a)
	bd, bdok := mapKeyAsDouble(b)
	if adok && bdok {
		return ad == bd
	}
	return false
}

func mapKeyAsInt64(k mapKey) (int64, bool) {
	switch k.kind {
	case keyInt:
		return k.i, true
	case keyUint:
		if k.u <= uint64(math.MaxInt64) {
			return int64(k.u), true
		}
		return 0, false
	case keyDouble:
		i, ok := float64IntegralInt64(k.d)
		return i, ok
	default:
		return 0, false
	}
}

func mapKeyAsDouble(k mapKey) (float64, bool) {
	switch k.kind {
	case keyDouble:
		return k.d, true
	case keyInt:
		return float64(k.i), true
	case keyUint:
		return float64(k.u), true
	default:
		return 0, false
	}
}

func nativeAnyMapEntries(a *sortedAdapter, m map[any]any) ([]sortedEntry, error) {
	entries := make([]sortedEntry, 0, len(m))
	for k, v := range m {
		rv := a.NativeToValue(k)
		mk, ok := classifyMapKey(rv)
		if !ok {
			return nil, fmt.Errorf("unsupported map key type")
		}
		entries = append(entries, sortedEntry{key: mk, val: a.NativeToValue(v)})
	}
	return entries, nil
}

func newSortedAnyMap(adapter *sortedAdapter, entries []sortedEntry) *sortedAnyMap {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].key.less(entries[j].key)
	})
	return &sortedAnyMap{adapter: adapter, entries: entries}
}

func extractMapperEntries(m traits.Mapper, adapter *sortedAdapter) ([]sortedEntry, error) {
	var entries []sortedEntry
	it := m.Iterator()
	for it.HasNext().Equal(types.True) == types.True {
		k := it.Next()
		mk, ok := classifyMapKey(k)
		if !ok {
			return nil, fmt.Errorf("unsupported map key type")
		}
		v, found := m.Find(k)
		if !found {
			return nil, fmt.Errorf("no such key")
		}
		entries = append(entries, sortedEntry{key: mk, val: v})
	}
	sz, ok := m.Size().(types.Int)
	if !ok {
		return nil, fmt.Errorf("unsupported map key type")
	}
	n := int(sz)
	if len(entries) != n {
		return nil, fmt.Errorf("unsupported map key type")
	}
	return entries, nil
}

func (m *sortedAnyMap) ConvertToNative(typeDesc reflect.Type) (any, error) {
	if typeDesc == nil || typeDesc == reflect.TypeOf(map[string]any{}) || typeDesc.Kind() == reflect.Interface {
		if len(m.entries) == 0 {
			return map[string]any{}, nil
		}
	}
	return nil, fmt.Errorf("type conversion error")
}

func (m *sortedAnyMap) ConvertToType(typeVal ref.Type) ref.Val {
	return types.NewErr("type conversion error")
}

func (m *sortedAnyMap) Equal(other ref.Val) ref.Val {
	switch ov := other.(type) {
	case *sortedStringMap:
		if len(m.entries) != len(ov.data) {
			return types.Bool(false)
		}
		for _, e := range m.entries {
			if e.key.kind != keyString {
				return types.Bool(false)
			}
			v, ok := ov.data[e.key.s]
			if !ok || e.val.Equal(m.adapter.NativeToValue(v)) != types.True {
				return types.Bool(false)
			}
		}
		return types.Bool(true)
	case *sortedAnyMap:
		if len(m.entries) != len(ov.entries) {
			return types.Bool(false)
		}
		for i := range m.entries {
			if !mapKeysEqual(m.entries[i].key, ov.entries[i].key) {
				return types.Bool(false)
			}
			if m.entries[i].val.Equal(ov.entries[i].val) != types.True {
				return types.Bool(false)
			}
		}
		return types.Bool(true)
	default:
		return types.Bool(false)
	}
}

func (m *sortedAnyMap) Type() ref.Type {
	return types.MapType
}

func (m *sortedAnyMap) Value() any { return m.entries }

func (m *sortedAnyMap) Contains(key ref.Val) ref.Val {
	_, ok := m.Find(key)
	return types.Bool(ok)
}

func (m *sortedAnyMap) Find(key ref.Val) (ref.Val, bool) {
	mk, ok := classifyMapKey(key)
	if !ok {
		return types.NewErr("no such key"), false
	}
	for _, e := range m.entries {
		if mapKeysEqual(e.key, mk) || mapKeysCrossEqual(e.key, mk) {
			return e.val, true
		}
	}
	return types.NewErr("no such key"), false
}

func (m *sortedAnyMap) Get(key ref.Val) ref.Val {
	v, ok := m.Find(key)
	if !ok {
		return types.NewErr("no such key")
	}
	return v
}

func (m *sortedAnyMap) Iterator() traits.Iterator {
	keys := make([]ref.Val, len(m.entries))
	for i, e := range m.entries {
		keys[i] = e.key.key
	}
	return &sortedAnyMapIter{keys: keys, idx: -1}
}

func (m *sortedAnyMap) Size() ref.Val {
	return types.Int(len(m.entries))
}

type sortedAnyMapIter struct {
	keys []ref.Val
	idx  int
}

func (it *sortedAnyMapIter) ConvertToNative(typeDesc reflect.Type) (any, error) {
	return nil, fmt.Errorf("type conversion on iterators not supported")
}

func (it *sortedAnyMapIter) ConvertToType(typeVal ref.Type) ref.Val {
	return types.NewErr("no such overload")
}

func (it *sortedAnyMapIter) Equal(other ref.Val) ref.Val {
	return types.NewErr("no such overload")
}

func (it *sortedAnyMapIter) Type() ref.Type {
	return types.IteratorType
}

func (it *sortedAnyMapIter) Value() any {
	return nil
}

func (it *sortedAnyMapIter) HasNext() ref.Val {
	return types.Bool(it.idx+1 < len(it.keys))
}

func (it *sortedAnyMapIter) Next() ref.Val {
	if it.idx+1 >= len(it.keys) {
		return types.NewErr("no such key")
	}
	it.idx++
	return it.keys[it.idx]
}
