package schema

import (
	"encoding/json"
	"reflect"

	gjschema "github.com/google/jsonschema-go/jsonschema"
)

var (
	byteSliceReflectType  = reflect.TypeOf([]byte(nil))
	rawMessageReflectType = reflect.TypeOf(json.RawMessage(nil))
)

// For generates a JSON Schema for T.
func For[T any]() (*Schema, error) {
	var z T
	return ForType(reflect.TypeOf(z))
}

// ForType generates a JSON Schema for a Go type.
func ForType(t reflect.Type) (*Schema, error) {
	if t == nil {
		return nil, invalidArg("nil type", nil)
	}
	if t.Kind() == reflect.Interface {
		return nil, invalidArg("type has no JSON form", map[string]any{"field": "interface{}"})
	}
	if path, ok := invalidFieldPath(t); ok {
		return nil, invalidArg("type has no JSON form", map[string]any{"field": path})
	}
	if err := validateMapKeys(t); err != nil {
		return nil, err
	}
	opts, err := generationOptions(t)
	if err != nil {
		return nil, err
	}
	s, err := gjschema.ForType(t, opts)
	if err != nil {
		return nil, invalidArg(err.Error(), nil)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, invalidArgf("marshal generated schema: %v", err)
	}
	raw = patchNullTypesInGeneratedSchema(raw)
	raw, err = patchEmbeddedJSONFields(raw, t, opts)
	if err != nil {
		return nil, err
	}
	raw, err = patchGeneratedSchema(raw, t, opts)
	if err != nil {
		return nil, err
	}
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

// patchNullTypesInGeneratedSchema walks the generated schema tree and unions null
// into slice types (except fixed-length [N]T arrays) and open string-keyed map
// types (additionalProperties true or a schema object). Closed struct objects
// and the root are never nullable; pointer fields keep generator-assigned null types.
func patchNullTypesInGeneratedSchema(raw json.RawMessage) json.RawMessage {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw
	}
	patchSchemaValue(doc, true)
	out, err := json.Marshal(doc)
	if err != nil {
		// Re-marshal can fail on unexpected library shapes; keep the raw schema.
		return raw
	}
	return out
}

func byteSliceSchema() *gjschema.Schema {
	return &gjschema.Schema{
		Types:           []string{"null", "string"},
		ContentEncoding: "base64",
	}
}

// rawMessageSchema accepts any JSON value encoding/json may store in RawMessage.
func rawMessageSchema() *gjschema.Schema {
	return &gjschema.Schema{}
}

func isByteSliceAlias(typ reflect.Type) bool {
	if typ == byteSliceReflectType || typ == rawMessageReflectType {
		return false
	}
	return typ.Kind() == reflect.Slice && typ.Elem().Kind() == reflect.Uint8 &&
		typ.Elem().PkgPath() == "" && typ.Elem().Size() == 1
}

func patchSchemaValue(v any, isRoot bool) {
	switch node := v.(type) {
	case map[string]any:
		if patchRootNull(node, isRoot) {
			patchTypeNullUnion(node)
		}
		if props, ok := node["properties"].(map[string]any); ok {
			for _, p := range props {
				patchSchemaValue(p, false)
			}
		}
		if items, ok := node["items"]; ok {
			patchSchemaValue(items, false)
		}
		if ap, ok := node["additionalProperties"]; ok {
			patchSchemaValue(ap, false)
		}
		if defs, ok := node["$defs"].(map[string]any); ok {
			for _, d := range defs {
				patchSchemaValue(d, false)
			}
		}
		if prefix, ok := node["prefixItems"].([]any); ok {
			for _, p := range prefix {
				patchSchemaValue(p, false)
			}
		}
	case []any:
		for _, e := range node {
			patchSchemaValue(e, false)
		}
	}
}

func patchTypeNullUnion(pm map[string]any) {
	t, ok := pm["type"].(string)
	if !ok {
		return
	}
	switch t {
	case "array":
		min, minOK := asInt(pm["minItems"])
		max, maxOK := asInt(pm["maxItems"])
		if !minOK || !maxOK || min != max {
			pm["type"] = []any{"null", "array"}
		}
	case "object":
		if openMapObject(pm) {
			pm["type"] = []any{"null", "object"}
		}
	}
}

func patchRootNull(node map[string]any, isRoot bool) bool {
	if !isRoot {
		return true
	}
	t, _ := node["type"].(string)
	switch t {
	case "array":
		return true
	case "object":
		return openMapObject(node)
	default:
		return false
	}
}

func openMapObject(pm map[string]any) bool {
	ap, ok := pm["additionalProperties"]
	if !ok {
		return false
	}
	if ap == false {
		return false
	}
	if ap == true {
		return true
	}
	_, isSchema := ap.(map[string]any)
	return isSchema
}

func invalidFieldPath(t reflect.Type) (string, bool) {
	seen := map[reflect.Type]bool{}
	return walkInvalidField(t, "", seen)
}

func walkInvalidField(t reflect.Type, prefix string, seen map[reflect.Type]bool) (string, bool) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if seen[t] {
		return "", false
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Chan, reflect.Func, reflect.Complex64, reflect.Complex128, reflect.UnsafePointer:
		if prefix == "" {
			return t.String(), true
		}
		return prefix, true
	case reflect.Struct:
		for _, f := range reflect.VisibleFields(t) {
			if !f.IsExported() {
				continue
			}
			if jsonName(f) == "" && (!f.Anonymous || !embedStructKind(f.Type)) {
				continue
			}
			next := f.Name
			if prefix != "" {
				next = prefix + "." + f.Name
			}
			if p, ok := walkInvalidField(f.Type, next, seen); ok {
				return p, true
			}
		}
	case reflect.Array, reflect.Slice:
		if p, ok := walkInvalidField(t.Elem(), prefix, seen); ok {
			return p, true
		}
	case reflect.Map:
		if p, ok := walkInvalidField(t.Elem(), prefix, seen); ok {
			return p, true
		}
	}
	return "", false
}

// validateMapKeys rejects a map whose key type encoding/json cannot write as a
// string, naming the field that holds it and the key type (REQ-SCHEMA-049).
func validateMapKeys(t reflect.Type) error {
	return walkMapKeys(t, "", map[reflect.Type]bool{})
}

func walkMapKeys(t reflect.Type, prefix string, seen map[reflect.Type]bool) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if seen[t] {
		return nil
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Struct:
		for _, f := range reflect.VisibleFields(t) {
			if !f.IsExported() || jsonName(f) == "" {
				continue
			}
			next := f.Name
			if prefix != "" {
				next = prefix + "." + f.Name
			}
			if err := walkMapKeys(f.Type, next, seen); err != nil {
				return err
			}
		}
	case reflect.Array, reflect.Slice:
		return walkMapKeys(t.Elem(), prefix, seen)
	case reflect.Map:
		if !mapKeyHasJSONText(t.Key()) {
			return invalidArg("map key type has no JSON text",
				map[string]any{"field": prefix, "key": t.Key().String()})
		}
		return walkMapKeys(t.Elem(), prefix, seen)
	}
	return nil
}

func mapKeyHasJSONText(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	}
	return t.Implements(textMarshalerReflectType)
}

func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	if tag == "" {
		return f.Name
	}
	name, _, hadComma := parseTag(tag)
	if name == "" {
		if hadComma {
			return f.Name
		}
		return f.Name
	}
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
