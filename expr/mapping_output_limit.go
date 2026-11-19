package expr

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
	_ "unsafe"

	"github.com/itchyny/gojq"
)

// gojq's unexported operator functions behind `+`, `-` and `*`, with their
// exact *big.Int arithmetic. Go fails the build when a gojq upgrade moves or
// renames one, instead of silently unbinding the output limit. go.mod pins
// gojq v0.12.20; go.sum moves with it.
//
//go:linkname gojqFuncOpAdd github.com/itchyny/gojq.funcOpAdd
func gojqFuncOpAdd(_, l, r any) any

//go:linkname gojqFuncOpSub github.com/itchyny/gojq.funcOpSub
func gojqFuncOpSub(_, l, r any) any

//go:linkname gojqFuncOpMul github.com/itchyny/gojq.funcOpMul
func gojqFuncOpMul(_, l, r any) any

func outputLimitError(limit int) error {
	return mappingEvalErr(fmt.Sprintf("output over the output limit %d", limit))
}

// outputLimitCompilerOptions registers the bounded functions. The rewrite
// above is the only caller: a mapping source cannot reach these names.
func outputLimitCompilerOptions(limit int) []gojq.CompilerOption {
	return []gojq.CompilerOption{
		gojq.WithFunction(boundedAdd, 2, 30, boundedAddFunc(limit)),
		gojq.WithFunction(boundedSub, 2, 30, boundedSubFunc(limit)),
		gojq.WithFunction(boundedMul, 2, 30, boundedMulFunc(limit)),
		gojq.WithFunction(boundedArray, 1, 1, boundedContainerFunc(limit)),
		gojq.WithFunction(boundedObject, 1, 1, boundedContainerFunc(limit)),
		gojq.WithFunction(boundedJoin, 2, 2, boundedJoinFunc(limit)),
		gojq.WithFunction(boundedImplode, 1, 1, boundedImplodeFunc(limit)),
		gojq.WithFunction(boundedBase64, 1, 1, boundedBase64Func(limit)),
		gojq.WithFunction(boundedBase64d, 1, 1, boundedBase64dFunc(limit)),
	}
}

// measureMappingValue fails when the RFC 8785 size of v is over limit. Every
// value a mapping yields passes through it, top-level or inside a container.
func measureMappingValue(v any, limit int) error {
	if err := addJSONSize(v, new(int), limit); err != nil {
		return outputLimitError(limit)
	}
	return nil
}

func boundedAddFunc(limit int) func(any, []any) any {
	return func(_ any, args []any) any {
		acc := args[0]
		for _, next := range args[1:] {
			if err := checkAddSize(acc, next, limit); err != nil {
				return err
			}
			acc = gojqFuncOpAdd(nil, acc, next)
			if err, ok := acc.(error); ok {
				return err
			}
			if err := measureMappingValue(acc, limit); err != nil {
				return err
			}
		}
		return acc
	}
}

func boundedSubFunc(limit int) func(any, []any) any {
	return func(_ any, args []any) any {
		acc := args[0]
		for _, next := range args[1:] {
			acc = gojqFuncOpSub(nil, acc, next)
			if err, ok := acc.(error); ok {
				return err
			}
			if err := measureMappingValue(acc, limit); err != nil {
				return err
			}
		}
		return acc
	}
}

func boundedMulFunc(limit int) func(any, []any) any {
	return func(_ any, args []any) any {
		acc := args[0]
		for _, next := range args[1:] {
			if err := checkMulSize(acc, next, limit); err != nil {
				return err
			}
			acc = gojqFuncOpMul(nil, acc, next)
			if err, ok := acc.(error); ok {
				return err
			}
			if err := measureMappingValue(acc, limit); err != nil {
				return err
			}
		}
		return acc
	}
}

// checkAddSize computes the size of l + r from the operands before gojq
// allocates the result.
func checkAddSize(l, r any, limit int) error {
	n := 0
	switch lv := l.(type) {
	case nil:
		return addJSONSize(r, &n, limit)
	case string:
		rv, ok := r.(string)
		if !ok {
			return nil // gojq reports the type error
		}
		n = stringJSONSize(lv) + stringJSONSize(rv) - 2
	case []any:
		rv, ok := r.([]any)
		if !ok {
			return nil
		}
		ls, err := exactJSONSize(lv)
		if err != nil {
			return err
		}
		rs, err := exactJSONSize(rv)
		if err != nil {
			return err
		}
		switch {
		case len(lv) == 0:
			n = rs
		case len(rv) == 0:
			n = ls
		default:
			// Two brackets go away and one comma joins the values.
			n = ls + rs - 1
		}
	case map[string]any:
		rv, ok := r.(map[string]any)
		if !ok {
			return nil
		}
		switch {
		case len(lv) == 0:
			return addJSONSize(rv, &n, limit)
		case len(rv) == 0:
			return addJSONSize(lv, &n, limit)
		}
		size, over := mergedObjectSize(lv, rv, limit)
		if over {
			return outputLimitError(limit)
		}
		n = size
	default:
		return nil
	}
	if n > limit {
		return outputLimitError(limit)
	}
	return nil
}

// checkMulSize computes the size of l * r before gojq builds it. Numbers are
// small; a repeated string and a deep-merged object are the cases that grow.
func checkMulSize(l, r any, limit int) error {
	if s, ok := l.(string); ok {
		if f, ok := gojq.ToFloat64(r); ok {
			return checkRepeatSize(s, f, limit)
		}
	}
	if s, ok := r.(string); ok {
		if f, ok := gojq.ToFloat64(l); ok {
			return checkRepeatSize(s, f, limit)
		}
	}
	lv, lok := l.(map[string]any)
	rv, rok := r.(map[string]any)
	if !lok || !rok {
		return nil
	}
	size, over := mergedObjectSize(lv, rv, limit)
	if over {
		return outputLimitError(limit)
	}
	if size > limit {
		return outputLimitError(limit)
	}
	return nil
}

func checkRepeatSize(s string, n float64, limit int) error {
	if n < 0 {
		return nil // gojq returns null
	}
	count := int(math.Min(n, math.MaxInt32))
	body := stringJSONSize(s) - 2
	size := int64(count)*int64(body) + 2
	if size > int64(limit) {
		return outputLimitError(limit)
	}
	return nil
}

func boundedContainerFunc(limit int) func(any, []any) any {
	return func(_ any, args []any) any {
		if err := measureMappingValue(args[0], limit); err != nil {
			return err
		}
		return args[0]
	}
}

func boundedJoinFunc(limit int) func(any, []any) any {
	return func(_ any, args []any) any {
		values, ok := mappingValues(args[0])
		if !ok {
			return fmt.Errorf("join cannot be applied to: %v", args[0])
		}
		sep, ok := args[1].(string)
		if !ok {
			return fmt.Errorf("join separator must be a string")
		}
		size := 2
		parts := make([]string, len(values))
		for i, v := range values {
			part, ok := joinPart(v)
			if !ok {
				return fmt.Errorf("join cannot be applied to: %v", v)
			}
			if i > 0 {
				size += stringJSONSize(sep) - 2
			}
			size += stringJSONSize(part) - 2
			parts[i] = part
		}
		if size > limit {
			return outputLimitError(limit)
		}
		return strings.Join(parts, sep)
	}
}

func boundedImplodeFunc(limit int) func(any, []any) any {
	return func(_ any, args []any) any {
		values, ok := args[0].([]any)
		if !ok {
			return fmt.Errorf("implode cannot be applied to: %v", args[0])
		}
		size := 2
		var sb strings.Builder
		sb.Grow(len(values))
		for _, v := range values {
			r, ok := gojq.ToInt(v)
			if !ok {
				return fmt.Errorf("implode cannot be applied to: %v", args[0])
			}
			if r < 0 || r > utf8.MaxRune {
				r = utf8.RuneError
			}
			size += utf8.RuneLen(rune(r))
			sb.WriteRune(rune(r))
		}
		if size > limit {
			return outputLimitError(limit)
		}
		return sb.String()
	}
}

func boundedBase64Func(limit int) func(any, []any) any {
	return func(_ any, args []any) any {
		s := mappingToString(args[0])
		size := 2 + base64.StdEncoding.EncodedLen(len(s))
		if size > limit {
			return outputLimitError(limit)
		}
		return base64.StdEncoding.EncodeToString([]byte(s))
	}
}

func boundedBase64dFunc(limit int) func(any, []any) any {
	return func(_ any, args []any) any {
		x := mappingToString(args[0])
		if i := strings.IndexRune(x, base64.StdPadding); i >= 0 {
			x = x[:i]
		}
		decoded, err := base64.RawStdEncoding.DecodeString(x)
		if err != nil {
			return fmt.Errorf("@base64d cannot be applied to: %v", args[0])
		}
		s := string(decoded)
		if stringJSONSize(s) > limit {
			return outputLimitError(limit)
		}
		return s
	}
}

func mappingValues(v any) ([]any, bool) {
	switch v := v.(type) {
	case []any:
		return v, true
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]any, len(keys))
		for i, k := range keys {
			out[i] = v[k]
		}
		return out, true
	default:
		return nil, false
	}
}

// joinPart renders one join element the way gojq does: strings as they are,
// booleans and numbers as JSON. Containers are an error, as in gojq's add.
func joinPart(v any) (string, bool) {
	switch t := v.(type) {
	case nil:
		return "", true
	case string:
		return t, true
	case bool, int, float64, *big.Int, json.Number:
		return mappingToString(t), true
	default:
		return "", false
	}
}

func mappingToString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return ""
	}
	return strings.TrimSuffix(sb.String(), "\n")
}

// addJSONSize adds the RFC 8785 size of v to *size and fails when it exceeds
// limit. A negative limit means no bound.
func addJSONSize(v any, size *int, limit int) error {
	if limit >= 0 && *size > limit {
		return errors.New("output over the output limit")
	}
	switch t := v.(type) {
	case nil:
		*size += 4
	case bool:
		if t {
			*size += 4
		} else {
			*size += 5
		}
	case int:
		*size += len(strconv.Itoa(t))
	case int64:
		*size += len(strconv.FormatInt(t, 10))
	case uint64:
		*size += len(strconv.FormatUint(t, 10))
	case *big.Int:
		*size += len(t.String())
	case float64:
		*size += len(formatJSONFloat(t))
	case json.Number:
		*size += len(t.String())
	case string:
		*size += stringJSONSize(t)
	case []any:
		*size++ // [
		for i, e := range t {
			if i > 0 {
				*size++
			}
			if err := addJSONSize(e, size, limit); err != nil {
				return err
			}
			if limit >= 0 && *size > limit {
				return errors.New("output over the output limit")
			}
		}
		*size++ // ]
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		*size++ // {
		for i, k := range keys {
			if i > 0 {
				*size++
			}
			*size += stringJSONSize(k) + 1 // key and colon
			if err := addJSONSize(t[k], size, limit); err != nil {
				return err
			}
			if limit >= 0 && *size > limit {
				return errors.New("output over the output limit")
			}
		}
		*size++ // }
	default:
		return fmt.Errorf("no JSON form")
	}
	if limit >= 0 && *size > limit {
		return errors.New("output over the output limit")
	}
	return nil
}

// exactJSONSize returns the RFC 8785 size of v without a bound.
func exactJSONSize(v any) (int, error) {
	n := 0
	if err := addJSONSize(v, &n, -1); err != nil {
		return 0, err
	}
	return n, nil
}

// stringJSONSize is the size of s encoded as an RFC 8785 string: its UTF-8
// bytes, escapes included, and two quotes.
func stringJSONSize(s string) int {
	n := 2
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\\':
			n += 2
		case c == '\b' || c == '\f' || c == '\n' || c == '\r' || c == '\t':
			n += 2
		case c < 0x20:
			n += 6
		default:
			n++
		}
	}
	return n
}

// mergedObjectSize is the size of the object gojq's deep merge produces,
// computed without building it.
func mergedObjectSize(a, b map[string]any, limit int) (int, bool) {
	size := 2
	first := true
	for k, va := range a {
		if !first {
			size++
		}
		first = false
		size += stringJSONSize(k) + 1
		if vb, ok := b[k]; ok {
			if ma, ok := va.(map[string]any); ok {
				if mb, ok := vb.(map[string]any); ok {
					n, over := mergedObjectSize(ma, mb, limit)
					if over {
						return 0, true
					}
					size += n
					continue
				}
			}
			size += objectValueSize(vb)
			continue
		}
		size += objectValueSize(va)
		if limit >= 0 && size > limit {
			return 0, true
		}
	}
	for k, vb := range b {
		if _, ok := a[k]; ok {
			continue
		}
		if !first {
			size++
		}
		first = false
		size += stringJSONSize(k) + 1 + objectValueSize(vb)
		if limit >= 0 && size > limit {
			return 0, true
		}
	}
	return size, limit >= 0 && size > limit
}

func objectValueSize(v any) int {
	n, err := exactJSONSize(v)
	if err != nil {
		return 0
	}
	return n
}

// formatJSONFloat is the ECMAScript Number::toString form RFC 8785 requires,
// matching gojq's encoder: -0 prints as 0, and a non-finite number as null.
func formatJSONFloat(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		return "0"
	}
	format := byte('f')
	if abs := math.Abs(f); abs < 1e-6 || abs >= 1e21 {
		format = 'e'
	}
	b := strconv.AppendFloat(nil, f, format, -1, 64)
	if format == 'e' {
		if n := len(b); n >= 4 && b[n-4] == 'e' && b[n-3] == '-' && b[n-2] == '0' {
			b[n-2] = b[n-1]
			b = b[:n-1]
		}
	}
	return string(b)
}
