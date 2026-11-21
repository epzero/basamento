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
	}
	rv := reflect.ValueOf(value)
	if rv.Kind() == reflect.Map {
		if rv.Type().Key().Kind() == reflect.String {
			out := make(map[string]any, rv.Len())
			iter := rv.MapRange()
			for iter.Next() {
				out[iter.Key().String()] = iter.Value().Interface()
			}
			return &sortedStringMap{adapter: a, data: out}
		}
		entries, err := nativeMapEntries(a, rv)
		if err != nil {
			return types.NewErrFromString(err.Error())
		}
		return newSortedAnyMap(a, entries)
	}
	return a.inner.NativeToValue(value)
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

// Fold visits the entries in key order. Comprehensions use it instead of
// looking each key up again.
func (m *sortedStringMap) Fold(f traits.Folder) {
	for _, k := range sortedStringKeys(m.data) {
		if !f.FoldEntry(types.String(k), m.adapter.NativeToValue(m.data[k])) {
			return
		}
	}
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
	// keyUnordered is a key whose CEL kind has no place in the ladder of
	// REQ-EXPR-021. It keeps its entry in the map (REQ-EXPR-022); iterating
	// over the map fails in the evaluator, not here.
	keyUnordered
)

type mapKey struct {
	kind mapKeyKind
	b    bool
	i    int64
	u    uint64
	d    float64
	s    string
	// unordered sorts keys of keyUnordered deterministically. It is only a
	// sort key, never an identity: distinct values can render alike, so
	// mapKeysEqual compares these keys by CEL equality instead.
	unordered string
	key       ref.Val
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
		if k == nil {
			return mapKey{}, false
		}
		return mapKey{
			kind:      keyUnordered,
			unordered: fmt.Sprintf("%T\x00%v", k, k.Value()),
			key:       k,
		}, true
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
	case keyUnordered:
		return k.unordered < o.unordered
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

// mapKeysEqual is key identity for lookup, membership and map equality
// (REQ-EXPR-021). An int, uint or double key and a key of another of those
// kinds with the same value are the same key; the conversion is lossless, so a
// double key of 1.5 or a uint beyond int64 matches no int. A key of no order is
// compared by CEL equality, not by the display string that only orders it.
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
		case keyUnordered:
			if a.key == nil || b.key == nil {
				return false
			}
			return a.key.Equal(b.key) == types.True
		default:
			return false
		}
	}
	equal, numeric := numericKeysEqual(a, b)
	return numeric && equal
}

// numericKeysEqual compares keys of the three numeric kinds, in either
// direction, when the conversion between them is lossless.
func numericKeysEqual(a, b mapKey) (equal, numeric bool) {
	switch {
	case a.kind == keyInt && b.kind == keyUint:
		u, ok := intToUintLossless(a.i)
		return ok && u == b.u, true
	case a.kind == keyUint && b.kind == keyInt:
		i, ok := uintToIntLossless(a.u)
		return ok && i == b.i, true
	case a.kind == keyDouble && b.kind == keyInt:
		i, ok := doubleToIntLossless(a.d)
		return ok && i == b.i, true
	case a.kind == keyInt && b.kind == keyDouble:
		i, ok := doubleToIntLossless(b.d)
		return ok && i == a.i, true
	case a.kind == keyDouble && b.kind == keyUint:
		u, ok := doubleToUintLossless(a.d)
		return ok && u == b.u, true
	case a.kind == keyUint && b.kind == keyDouble:
		u, ok := doubleToUintLossless(b.d)
		return ok && u == a.u, true
	default:
		return false, false
	}
}

func intToUintLossless(i int64) (uint64, bool) {
	if i < 0 {
		return 0, false
	}
	return uint64(i), true
}

func uintToIntLossless(u uint64) (int64, bool) {
	if u > math.MaxInt64 {
		return 0, false
	}
	return int64(u), true
}

func doubleToIntLossless(f float64) (int64, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < -0x1p63 || f >= 0x1p63 {
		return 0, false
	}
	i := int64(f)
	if float64(i) != f {
		return 0, false
	}
	return i, true
}

func doubleToUintLossless(f float64) (uint64, bool) {
	if math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f >= 0x1p64 {
		return 0, false
	}
	u := uint64(f)
	if float64(u) != f {
		return 0, false
	}
	return u, true
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

// nativeMapEntries converts any typed Go map into sorted entries, so every
// map an expression sees has the same key order whatever its Go key type.
func nativeMapEntries(a *sortedAdapter, rv reflect.Value) ([]sortedEntry, error) {
	entries := make([]sortedEntry, 0, rv.Len())
	iter := rv.MapRange()
	for iter.Next() {
		mk, ok := classifyMapKey(a.NativeToValue(iter.Key().Interface()))
		if !ok {
			return nil, fmt.Errorf("unsupported map key type")
		}
		entries = append(entries, sortedEntry{key: mk, val: a.NativeToValue(iter.Value().Interface())})
	}
	return entries, nil
}

func newSortedAnyMap(adapter *sortedAdapter, entries []sortedEntry) *sortedAnyMap {
	// Stable: keys of no order share one display sort key, and a map holding
	// one cannot be iterated, so their relative order only has to stay fixed.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].key.less(entries[j].key)
	})
	return &sortedAnyMap{adapter: adapter, entries: entries}
}

func extractMapperEntries(m traits.Mapper, adapter *sortedAdapter) ([]sortedEntry, error) {
	if foldable, ok := m.(traits.Foldable); ok {
		f := &mapEntryFolder{adapter: adapter}
		foldable.Fold(f)
		if f.err != nil {
			return nil, f.err
		}
		sz, ok := m.Size().(types.Int)
		if !ok || int(sz) != len(f.entries) {
			return nil, fmt.Errorf("unsupported map key type")
		}
		return f.entries, nil
	}
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

// mapEntryFolder collects a map's entries from its Fold, which yields each
// key with its value. Looking keys up again would lose a NaN key, which never
// equals itself.
type mapEntryFolder struct {
	adapter *sortedAdapter
	entries []sortedEntry
	err     error
}

func (f *mapEntryFolder) FoldEntry(key, val any) bool {
	if f.err != nil {
		return false
	}
	k, ok := key.(ref.Val)
	if !ok {
		k = f.adapter.NativeToValue(key)
	}
	v, ok := val.(ref.Val)
	if !ok {
		v = f.adapter.NativeToValue(val)
	}
	mk, ok := classifyMapKey(k)
	if !ok {
		f.err = fmt.Errorf("unsupported map key type")
		return false
	}
	f.entries = append(f.entries, sortedEntry{key: mk, val: v})
	return true
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

// hasUnordered reports whether the map holds a key of no order (REQ-EXPR-022).
func (m *sortedAnyMap) hasUnordered() bool {
	for _, e := range m.entries {
		if e.key.kind == keyUnordered {
			return true
		}
	}
	return false
}

func (m *sortedAnyMap) Equal(other ref.Val) ref.Val {
	if m.hasUnordered() {
		return types.NewErr("unordered key")
	}
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
		if ov.hasUnordered() {
			return types.NewErr("unordered key")
		}
		if len(m.entries) != len(ov.entries) {
			return types.Bool(false)
		}
		// Keys can sort into different positions when their kinds differ
		// (int < uint < double), so match by key, not by index. Each right
		// entry is consumed once: a lookup that left it matchable would let
		// two left keys share one right entry, so two different key sets could
		// compare equal.
		used := make([]bool, len(ov.entries))
		for _, e := range m.entries {
			match := consumeMatchingEntry(ov.entries, used, e.key)
			if match < 0 || e.val.Equal(ov.entries[match].val) != types.True {
				return types.Bool(false)
			}
			used[match] = true
		}
		return types.Bool(true)
	default:
		return types.Bool(false)
	}
}

// consumeMatchingEntry returns the index of the first unused entry whose key
// equals key, preferring one of the same kind, or -1. Callers mark it used, so
// two left keys can never share one right entry.
func consumeMatchingEntry(entries []sortedEntry, used []bool, key mapKey) int {
	for i, oe := range entries {
		if used[i] || oe.key.kind != key.kind || !mapKeysEqual(key, oe.key) {
			continue
		}
		return i
	}
	for i, oe := range entries {
		if used[i] || !mapKeysEqual(key, oe.key) {
			continue
		}
		return i
	}
	return -1
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
	// A key of the same kind wins over a numerically equal key of another
	// kind, as in stock cel-go: a literal can hold both although they are the
	// same key (REQ-EXPR-021).
	for _, e := range m.entries {
		if e.key.kind == mk.kind && mapKeysEqual(e.key, mk) {
			return e.val, true
		}
	}
	for _, e := range m.entries {
		if mapKeysEqual(e.key, mk) {
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

// Fold visits the entries in key order. Comprehensions use it instead of
// looking each key up again, so a NaN key still yields its value.
func (m *sortedAnyMap) Fold(f traits.Folder) {
	for _, e := range m.entries {
		if !f.FoldEntry(e.key.key, e.val) {
			return
		}
	}
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
