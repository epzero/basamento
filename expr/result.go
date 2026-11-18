package expr

import (
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
)

func celValToJSON(val ref.Val) (any, error) {
	if types.IsError(val) {
		msg := val.(*types.Err).Error()
		cause := mapCELCause(msg)
		if cause != "" {
			return nil, errors.New(cause)
		}
		return nil, errors.New(msg)
	}
	if val.Type() == types.TypeType {
		return nil, fmt.Errorf("no JSON form")
	}
	switch v := val.(type) {
	case types.Bool:
		return bool(v), nil
	case types.String:
		return string(v), nil
	case types.Int:
		return int64(v), nil
	case types.Uint:
		return int64(v), nil
	case types.Double:
		f := float64(v)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("no JSON form")
		}
		return f, nil
	case types.Null:
		return nil, nil
	case types.Timestamp:
		return formatTimestampUTC(v.Time), nil
	case types.Duration:
		return v.ConvertToType(types.StringType).Value(), nil
	case types.Bytes:
		return base64.StdEncoding.EncodeToString(v), nil
	case *jsonObjectVal:
		return sortObjectMap(v.data), nil
	case *sortedStringMap:
		return sortObjectMap(v.data), nil
	}
	if l, ok := val.(traits.Lister); ok {
		return listToJSON(l)
	}
	if m, ok := val.(traits.Mapper); ok {
		return mapperToJSON(m)
	}
	if opt, ok := val.(*types.Optional); ok {
		if !opt.HasValue() {
			return nil, fmt.Errorf("no JSON form")
		}
		return celValToJSON(opt.GetValue())
	}
	n, err := val.ConvertToNative(reflect.TypeOf((*any)(nil)).Elem())
	if err != nil {
		return nil, fmt.Errorf("no JSON form")
	}
	return normalizeNativeJSON(n)
}

func listToJSON(l traits.Lister) ([]any, error) {
	n := int(l.Size().(types.Int))
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		elem := l.Get(types.Int(i))
		j, err := celValToJSON(elem)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

func mapperToJSON(m traits.Mapper) (map[string]any, error) {
	out := map[string]any{}
	it := m.Iterator()
	for it.HasNext().Equal(types.True) == types.True {
		k := it.Next()
		ks, ok := k.(types.String)
		if !ok {
			return nil, fmt.Errorf("no JSON form")
		}
		v, found := m.Find(k)
		if !found {
			return nil, fmt.Errorf("no such key")
		}
		jv, err := celValToJSON(v)
		if err != nil {
			return nil, err
		}
		out[string(ks)] = jv
	}
	return sortObjectMap(out), nil
}

func normalizeNativeJSON(v any) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		return sortObjectMap(t), nil
	case map[any]any:
		return nil, fmt.Errorf("no JSON form")
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			ne, err := normalizeNativeJSON(e)
			if err != nil {
				return nil, err
			}
			out[i] = ne
		}
		return out, nil
	case time.Time:
		return formatTimestampUTC(t), nil
	default:
		return v, nil
	}
}

func sortObjectMap(m map[string]any) map[string]any {
	if m == nil {
		return m
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(m))
	for _, k := range keys {
		out[k] = m[k]
	}
	return out
}

func formatTimestampUTC(t time.Time) string {
	t = t.UTC()
	s := t.Format(time.RFC3339Nano)
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		frac := strings.TrimRight(s[dot+1:len(s)-1], "0")
		if frac == "" {
			return t.Format("2006-01-02T15:04:05Z")
		}
		return s[:dot] + "." + frac + "Z"
	}
	if !strings.HasSuffix(s, "Z") {
		return t.Format("2006-01-02T15:04:05Z")
	}
	return s
}
