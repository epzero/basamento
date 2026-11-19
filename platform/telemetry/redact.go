package telemetry

import (
	"reflect"
	"strings"
	"sync"
)

// Redacted is the literal that replaces a credential value or a sensitive
// field's value (REQ-TELEM-006).
const Redacted = "[REDACTED]"

var sensitiveKeys = map[string]bool{
	"password":      true,
	"secret":        true,
	"token":         true,
	"authorization": true,
}

// SecretsSource supplies credential values whose occurrences are removed from
// every recorded signal. Implementations must be safe for concurrent use.
type SecretsSource interface {
	CredentialValues() []string
}

// StaticSecrets is a SecretsSource over values added at runtime. It is safe
// for concurrent use and is what Config.Secrets falls back to.
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
	if sensitiveKeys[strings.ToLower(key)] {
		return true
	}
	if !strings.Contains(key, ".") {
		return false
	}
	for _, part := range strings.Split(key, ".") {
		if sensitiveKeys[strings.ToLower(part)] {
			return true
		}
	}
	return false
}

// Value redacts v under key, descending into maps, slices and pointers so a
// sensitive field is found at any depth.
func (r redactor) Value(key string, v any) any {
	if r.Key(key) {
		return Redacted
	}
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return r.String(x)
	case []byte:
		return x
	case map[string]any:
		return r.Map(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = r.Value(key, item)
		}
		return out
	}
	return r.reflectValue(key, v)
}

// Map redacts every value of m, including nested maps and slices.
func (r redactor) Map(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = r.Value(k, v)
	}
	return out
}

func (r redactor) reflectValue(key string, v any) any {
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return v
		}
		return r.Value(key, rv.Elem().Interface())
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return v
		}
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			k := iter.Key().String()
			out[k] = r.Value(k, iter.Value().Interface())
		}
		return out
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out[i] = r.Value(key, rv.Index(i).Interface())
		}
		return out
	default:
		return v
	}
}

// AttrValue is unused by the package today; it exists so callers that already
// have a converted attribute value redact it the same way as a label.
func (r redactor) AttrValue(key string, v any) any {
	return r.Value(key, v)
}
