package expr

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
)

func celValToJSON(val ref.Val) (any, error) {
	return celValToJSONWalk(val, newJSONWalk())
}

func celValToJSONWalk(val ref.Val, w *jsonWalk) (any, error) {
	if types.IsError(val) {
		cause := mapCELCause(val.(*types.Err).Error())
		if cause == "" {
			cause = "evaluation failed"
		}
		return nil, errors.New(cause)
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
		u := uint64(v)
		if u > math.MaxInt64 {
			return json.Number(strconv.FormatUint(u, 10)), nil
		}
		return int64(u), nil
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
		return jsonObjectValToJSON(v, w)
	case *sortedStringMap:
		return stringMapDataToJSON(v.data, w)
	case *sortedAnyMap:
		if len(v.entries) == 0 {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("no JSON form")
	}
	if l, ok := val.(traits.Lister); ok {
		return listToJSON(l, w)
	}
	if m, ok := val.(traits.Mapper); ok {
		return mapperToJSON(m, w)
	}
	if opt, ok := val.(*types.Optional); ok {
		if !opt.HasValue() {
			return nil, fmt.Errorf("no JSON form")
		}
		return celValToJSONWalk(opt.GetValue(), w)
	}
	n, err := val.ConvertToNative(reflect.TypeOf((*any)(nil)).Elem())
	if err != nil {
		return nil, fmt.Errorf("no JSON form")
	}
	return normalizeNativeJSON(n, w)
}

func listToJSON(l traits.Lister, w *jsonWalk) ([]any, error) {
	sz, ok := l.Size().(types.Int)
	if !ok {
		return nil, fmt.Errorf("no JSON form")
	}
	n := int(sz)
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		elem := l.Get(types.Int(i))
		j, err := celValToJSONWalk(elem, w)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

func mapperToJSON(m traits.Mapper, w *jsonWalk) (map[string]any, error) {
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
		jv, err := celValToJSONWalk(v, w)
		if err != nil {
			return nil, err
		}
		out[string(ks)] = jv
	}
	return out, nil
}

func normalizeNativeJSON(v any, w *jsonWalk) (any, error) {
	switch t := v.(type) {
	case map[string]any:
		return bindingValueToJSON(t, w)
	case map[any]any:
		return nil, fmt.Errorf("no JSON form")
	case []any:
		return bindingValueToJSON(t, w)
	case time.Time:
		return formatTimestampUTC(t), nil
	default:
		return v, nil
	}
}

func stringMapDataToJSON(data map[string]any, w *jsonWalk) (map[string]any, error) {
	ptr := addrOf(data)
	if err := w.down(ptr); err != nil {
		return nil, err
	}
	defer w.up(ptr)
	out := make(map[string]any, len(data))
	for _, k := range sortedStringKeys(data) {
		jv, err := bindingValueToJSON(data[k], w)
		if err != nil {
			return nil, err
		}
		out[k] = jv
	}
	return out, nil
}

func jsonObjectValToJSON(o *jsonObjectVal, w *jsonWalk) (map[string]any, error) {
	if o == nil {
		return nil, nil
	}
	ptr := addrOf(o)
	if err := w.down(ptr); err != nil {
		return nil, err
	}
	defer w.up(ptr)
	out := make(map[string]any, len(o.data))
	for _, k := range sortedStringKeys(o.data) {
		jv, err := bindingValueToJSON(o.data[k], w)
		if err != nil {
			return nil, err
		}
		out[k] = jv
	}
	return out, nil
}

func bindingValueToJSON(v any, w *jsonWalk) (any, error) {
	if w == nil {
		w = newJSONWalk()
	}
	switch t := v.(type) {
	case *jsonObjectVal:
		return jsonObjectValToJSON(t, w)
	case *sortedStringMap:
		return stringMapDataToJSON(t.data, w)
	case *sortedAnyMap:
		if len(t.entries) == 0 {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("no JSON form")
	case map[string]any:
		ptr := addrOf(t)
		if err := w.down(ptr); err != nil {
			return nil, err
		}
		defer w.up(ptr)
		out := make(map[string]any, len(t))
		for _, k := range sortedStringKeys(t) {
			jv, err := bindingValueToJSON(t[k], w)
			if err != nil {
				return nil, err
			}
			out[k] = jv
		}
		return out, nil
	case []any:
		ptr := addrOf(t)
		if err := w.down(ptr); err != nil {
			return nil, err
		}
		defer w.up(ptr)
		out := make([]any, len(t))
		for i, e := range t {
			jv, err := bindingValueToJSON(e, w)
			if err != nil {
				return nil, err
			}
			out[i] = jv
		}
		return out, nil
	case ref.Val:
		return celValToJSONWalk(t, w)
	default:
		return bindScalarToJSON(v)
	}
}

func bindScalarToJSON(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	switch t := v.(type) {
	case bool, string, int, int64, uint64, float64, json.Number:
		return t, nil
	case time.Time:
		return formatTimestampUTC(t), nil
	default:
		if rv, ok := v.(ref.Val); ok {
			return celValToJSONWalk(rv, newJSONWalk())
		}
		return t, nil
	}
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
