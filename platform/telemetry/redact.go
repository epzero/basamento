package telemetry

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// Redacted is the literal that replaces a credential value or a sensitive
// field's value (REQ-TELEM-006).
const Redacted = "[REDACTED]"

// maxRedactDepth bounds reflection over nested values. A value deeper than this
// is redacted entire rather than descending further, which also breaks cycles.
const maxRedactDepth = 64

func sensitiveKey(key string) bool {
	switch strings.ToLower(key) {
	case "password", "secret", "token", "authorization":
		return true
	default:
		return false
	}
}

// SecretsSource supplies credential values whose occurrences are removed from
// every recorded signal. Implementations must be safe for concurrent use.
type SecretsSource interface {
	CredentialValues() []string
}

// StaticSecrets is a SecretsSource over values added at runtime. It is safe
// for concurrent use; a Setup keeps one for SetCredential.
type StaticSecrets struct {
	mu     sync.RWMutex
	values []string
}

// NewStaticSecrets returns a source holding values. Empty values are ignored:
// matching one would redact every string.
func NewStaticSecrets(values ...string) *StaticSecrets {
	s := &StaticSecrets{}
	for _, v := range values {
		s.Add(v)
	}
	return s
}

// Add records value as a credential to redact. An empty value is ignored.
func (s *StaticSecrets) Add(value string) {
	if value == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.values {
		if existing == value {
			return
		}
	}
	s.values = append(s.values, value)
}

// CredentialValues returns a copy of the values to redact.
func (s *StaticSecrets) CredentialValues() []string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.values))
	copy(out, s.values)
	return out
}

// redactor replaces credential values and sensitive field values on their way
// into a kept or exported signal (REQ-TELEM-006, REQ-TELEM-007).
type redactor struct {
	sources []SecretsSource
}

func (r redactor) values() []string {
	var out []string
	for _, src := range r.sources {
		if src == nil {
			continue
		}
		out = append(out, src.CredentialValues()...)
	}
	return out
}

// String replaces the whole value with Redacted when it contains a credential
// value. A partial replacement would leave the credential in the signal.
func (r redactor) String(s string) string {
	if s == "" {
		return s
	}
	for _, value := range r.values() {
		if value != "" && strings.Contains(s, value) {
			return Redacted
		}
	}
	return s
}

// Key reports whether key names a sensitive field. The key is read as a
// dotted path so both a flat "user.password" attribute and a nested
// {"user": {"password": …}} group are covered. A segment that merely contains
// a sensitive word, such as "token_count", is not sensitive (TELEM-006-04).
func (r redactor) Key(key string) bool {
	if sensitiveKey(key) {
		return true
	}
	if !strings.Contains(key, ".") {
		return false
	}
	for _, part := range strings.Split(key, ".") {
		if sensitiveKey(part) {
			return true
		}
	}
	return false
}

// Value redacts v under key, descending into maps, slices, pointers and structs
// so a sensitive field is found at any depth. Every value it returns is one the
// exporter can encode: no struct, error, fmt.Stringer or []byte survives
// unchanged (REQ-TELEM-006, REQ-TELEM-007).
func (r redactor) Value(key string, v any) any {
	return r.valueAt(key, v, 0)
}

func (r redactor) valueAt(key string, v any, depth int) any {
	if r.Key(key) {
		return Redacted
	}
	if depth > maxRedactDepth {
		return Redacted
	}
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return r.String(x)
	case []byte:
		return r.String(string(x))
	case error:
		if nilValue(v) {
			return nil
		}
		if !opaqueTextValue(v) {
			return r.reflectValueAt(v, depth+1)
		}
		// A wrapper such as fmt.wrapError or errors.joinError has only
		// unexported fields, so it looks opaque. Its text would render the
		// wrapped error, which may be a struct with a sensitive field.
		if wrappedNonOpaque(x, 0) {
			return Redacted
		}
		return r.String(x.Error())
	case fmt.Stringer:
		if nilValue(v) {
			return nil
		}
		if opaqueTextValue(v) {
			return r.String(x.String())
		}
		return r.reflectValueAt(v, depth+1)
	case map[string]any:
		return r.mapAt(x, depth+1)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = r.valueAt("", item, depth+1)
		}
		return out
	}
	return r.reflectValueAt(v, depth+1)
}

// Map redacts every value of m, including nested maps, slices and structs.
func (r redactor) Map(m map[string]any) map[string]any {
	return r.mapAt(m, 0)
}

func (r redactor) mapAt(m map[string]any, depth int) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = r.valueAt(k, v, depth+1)
	}
	return out
}

// opaqueTextValue reports whether v must be rendered through its Error or
// String method instead of reflection. A struct with an exported or
// sensitive-named field is not opaque: reflection must see that field's name,
// or a Stringer would bypass field-name redaction. Unexported embedded or
// struct-typed fields are inspected too, so a payload hidden behind them is
// not declared opaque. time.Time and similar structs with only unexported,
// non-sensitive fields keep their text form.
func opaqueTextValue(v any) bool {
	return opaqueReflectValue(reflect.ValueOf(v))
}

func opaqueReflectValue(rv reflect.Value) bool {
	for rv.IsValid() && rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return true
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() || rv.Kind() != reflect.Struct {
		return true
	}
	return opaqueType(rv.Type(), 0, map[reflect.Type]bool{})
}

// opaqueType reports whether every field of typ is unexported and neither
// sensitive-named nor a struct that could carry such a field. It descends into
// embedded fields and unexported value structs, but not unexported named
// pointers: time.Time's *time.Location must stay opaque.
func opaqueType(typ reflect.Type, depth int, seen map[reflect.Type]bool) bool {
	if depth > maxRedactDepth {
		return false
	}
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() != reflect.Struct {
		return true
	}
	if seen[typ] {
		return true
	}
	seen[typ] = true
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.PkgPath == "" || sensitiveKey(field.Name) {
			return false
		}
		if field.Anonymous || field.Type.Kind() == reflect.Struct {
			if !opaqueType(field.Type, depth+1, seen) {
				return false
			}
		}
	}
	return true
}

// wrappedNonOpaque reports whether any error in the unwrap chain is a struct
// with exported or sensitive fields. The wrapper's own text is not safe in that
// case, because it would render that struct through its Error method.
func wrappedNonOpaque(err error, depth int) bool {
	if depth > maxRedactDepth {
		return true
	}
	var single interface{ Unwrap() error }
	if errors.As(err, &single) {
		inner := single.Unwrap()
		if inner == nil {
			return false
		}
		if !opaqueTextValue(inner) {
			return true
		}
		return wrappedNonOpaque(inner, depth+1)
	}
	var multi interface{ Unwrap() []error }
	if errors.As(err, &multi) {
		for _, inner := range multi.Unwrap() {
			if inner == nil {
				continue
			}
			if !opaqueTextValue(inner) || wrappedNonOpaque(inner, depth+1) {
				return true
			}
		}
	}
	return false
}

func nilValue(v any) bool {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return rv.IsNil()
	default:
		return false
	}
}

func (r redactor) reflectValueAt(v any, depth int) any {
	return r.reflectValue(reflect.ValueOf(v), depth)
}

// reflectValue walks a value without calling Interface on a field the caller
// cannot address. That lets it read an exported Password inside an unexported
// embedded struct or named unexported struct field instead of falling back to
// a String method that would leak it.
func (r redactor) reflectValue(rv reflect.Value, depth int) any {
	if depth > maxRedactDepth || !rv.IsValid() {
		return Redacted
	}
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return r.reflectValue(rv.Elem(), depth+1)
	case reflect.String:
		return r.String(rv.String())
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		if rv.CanInterface() {
			return rv.Interface()
		}
		switch rv.Kind() {
		case reflect.Bool:
			return rv.Bool()
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			return rv.Int()
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			return rv.Uint()
		default:
			return rv.Float()
		}
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return Redacted
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			k := iter.Key().String()
			if r.Key(k) {
				out[k] = Redacted
				continue
			}
			out[k] = r.itemValue(iter.Value(), k, depth)
		}
		return out
	case reflect.Slice:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			raw := make([]byte, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				raw[i] = byte(rv.Index(i).Uint())
			}
			return r.String(string(raw))
		}
		fallthrough
	case reflect.Array:
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = r.itemValue(rv.Index(i), "", depth)
		}
		return out
	case reflect.Struct:
		out := make(map[string]any, rv.NumField())
		typ := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			field := typ.Field(i)
			value := rv.Field(i)
			if sensitiveKey(field.Name) {
				if field.PkgPath != "" {
					// An unexported sensitive field cannot be rendered
					// partially without risking the rest of the struct.
					return Redacted
				}
				out[field.Name] = Redacted
				continue
			}
			if field.PkgPath == "" && value.CanInterface() {
				out[field.Name] = r.valueAt(field.Name, value.Interface(), depth+1)
				continue
			}
			if field.Type.Kind() == reflect.Struct || (field.Type.Kind() == reflect.Pointer && field.Type.Elem().Kind() == reflect.Struct) {
				if opaqueReflectValue(value) {
					// No text method can be called safely here.
					out[field.Name] = Redacted
					continue
				}
			}
			out[field.Name] = r.itemValue(value, field.Name, depth)
		}
		return out
	default:
		return Redacted
	}
}

func (r redactor) itemValue(value reflect.Value, key string, depth int) any {
	if value.CanInterface() {
		return r.valueAt(key, value.Interface(), depth+1)
	}
	return r.reflectValue(value, depth+1)
}
