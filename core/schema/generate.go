package schema

import (
	"encoding/json"
	"fmt"
	"reflect"

	gjschema "github.com/google/jsonschema-go/jsonschema"
)

// For generates a JSON Schema for T.
func For[T any]() (*Schema, error) {
	var z T
	return ForType(reflect.TypeOf(z))
}

// ForType generates a JSON Schema for a Go type.
func ForType(t reflect.Type) (*Schema, error) {
	if path, ok := invalidFieldPath(t); ok {
		return nil, invalidArg("type has no JSON form", map[string]any{"field": path})
	}
	s, err := gjschema.ForType(t, &gjschema.ForOptions{})
	if err != nil {
		return nil, invalidArg(err.Error(), nil)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, invalidArgf("marshal generated schema: %v", err)
	}
	raw = patchSliceNullTypes(raw)
	canonical, err := Canonicalize(raw)
	if err != nil {
		return nil, err
	}
	return &Schema{Raw: canonical}, nil
}

func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	default:
		return 0, false
	}
}

func patchSliceNullTypes(raw json.RawMessage) json.RawMessage {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw
	}
	props, _ := doc["properties"].(map[string]any)
	for name, p := range props {
		pm, _ := p.(map[string]any)
		if pm == nil {
			continue
		}
		switch t := pm["type"].(type) {
		case string:
			switch t {
			case "array":
				min, minOK := asInt(pm["minItems"])
				max, maxOK := asInt(pm["maxItems"])
				if !(minOK && maxOK && min == max) {
					pm["type"] = []any{"null", "array"}
				}
			case "object":
				if pm["additionalProperties"] != nil {
					pm["type"] = []any{"null", "object"}
				}
			}
			props[name] = pm
		}
	}
	doc["properties"] = props
	out, err := json.Marshal(doc)
	if err != nil {
		return raw
	}
	return out
}

func invalidFieldPath(t reflect.Type) (string, bool) {
	return walkInvalidField(t, "")
}

func walkInvalidField(t reflect.Type, prefix string) (string, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Chan, reflect.Func, reflect.Complex64, reflect.Complex128, reflect.UnsafePointer:
		if prefix == "" {
			return t.String(), true
		}
		return prefix, true
	case reflect.Struct:
		for _, f := range reflect.VisibleFields(t) {
			if f.Anonymous {
				continue
			}
			if jsonName(f) == "" {
				continue
			}
			next := f.Name
			if prefix != "" {
				next = prefix + "." + f.Name
			}
			if p, ok := walkInvalidField(f.Type, next); ok {
				return p, true
			}
		}
	case reflect.Array, reflect.Slice:
		if p, ok := walkInvalidField(t.Elem(), prefix); ok {
			return p, true
		}
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			if prefix == "" {
				return fmt.Sprintf("map key %s", t.Key().Kind()), true
			}
			return prefix, true
		}
		if p, ok := walkInvalidField(t.Elem(), prefix); ok {
			return p, true
		}
	}
	return "", false
}

func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	if tag == "" {
		return f.Name
	}
	name, _, _ := parseTag(tag)
	return name
}

func parseTag(tag string) (string, map[string]bool, bool) {
	if idx := indexComma(tag); idx >= 0 {
		settings := map[string]bool{}
		for _, p := range splitComma(tag[idx+1:]) {
			if p != "" {
				settings[p] = true
			}
		}
		return tag[:idx], settings, true
	}
	return tag, nil, false
}

func indexComma(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			return i
		}
	}
	return -1
}

func splitComma(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}
