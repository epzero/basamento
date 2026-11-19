package schema

import (
	"encoding"
	"encoding/json"
	"reflect"
	"time"

	gjschema "github.com/google/jsonschema-go/jsonschema"
)

var (
	textMarshalerReflectType = reflect.TypeFor[encoding.TextMarshaler]()
	jsonMarshalerReflectType = reflect.TypeFor[json.Marshaler]()
	timeReflectType          = reflect.TypeFor[time.Time]()
	numberReflectType        = reflect.TypeFor[json.Number]()
)

// encodingRule is how encoding/json encodes a value before it looks at the
// type's structure (REQ-SCHEMA-039 to REQ-SCHEMA-045).
type encodingRule int

const (
	ruleNone    encodingRule = iota
	ruleRaw                  // json.RawMessage: every JSON value
	ruleTime                 // time.Time: a string
	ruleNumber               // json.Number: a number
	ruleJSON                 // json.Marshaler: every JSON value
	ruleText                 // encoding.TextMarshaler: a string
	rulePointer              // only the pointer implements an encoder: every JSON value
	ruleBase64               // a named []byte without its own encoding: nullable base64 string
)

// typeEncoding applies the design's order: the types whose encoding is known,
// then the value's method set, then the pointer's, then the byte-slice rule.
func typeEncoding(t reflect.Type) encodingRule {
	base := t
	for base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	switch base {
	case rawMessageReflectType:
		return ruleRaw
	case timeReflectType:
		return ruleTime
	case numberReflectType:
		return ruleNumber
	}
	if base.Implements(jsonMarshalerReflectType) {
		return ruleJSON
	}
	if base.Implements(textMarshalerReflectType) {
		return ruleText
	}
	ptr := reflect.PointerTo(base)
	if ptr.Implements(jsonMarshalerReflectType) || ptr.Implements(textMarshalerReflectType) {
		return rulePointer
	}
	if isByteSliceAlias(base) {
		return ruleBase64
	}
	return ruleNone
}

func (r encodingRule) schemaFor() *gjschema.Schema {
	switch r {
	case ruleRaw, ruleJSON, rulePointer:
		return &gjschema.Schema{}
	case ruleTime, ruleText:
		return &gjschema.Schema{Type: "string"}
	case ruleNumber:
		return &gjschema.Schema{Type: "number"}
	case ruleBase64:
		return byteSliceSchema()
	}
	return nil
}

// generationOptions overrides every type whose encoding the generator must
// describe, wherever it appears. Maps with a key encoding/json writes as a
// string get an object schema because the library only accepts string keys.
func generationOptions(root reflect.Type) (*gjschema.ForOptions, error) {
	ts := map[reflect.Type]*gjschema.Schema{
		byteSliceReflectType:  byteSliceSchema(),
		rawMessageReflectType: rawMessageSchema(),
	}
	needMap := map[reflect.Type]bool{}
	seen := map[reflect.Type]bool{}
	var visit func(reflect.Type) error
	visit = func(t reflect.Type) error {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if seen[t] {
			return nil
		}
		seen[t] = true
		if r := typeEncoding(t); r != ruleNone {
			ts[t] = r.schemaFor()
		}
		switch t.Kind() {
		case reflect.Struct:
			for _, f := range reflect.VisibleFields(t) {
				if !f.IsExported() && (!f.Anonymous || !embedStructKind(f.Type)) {
					continue
				}
				if jsonName(f) == "" && (!f.Anonymous || !embedStructKind(f.Type)) {
					continue
				}
				if err := visit(f.Type); err != nil {
					return err
				}
			}
		case reflect.Slice, reflect.Array:
			return visit(t.Elem())
		case reflect.Map:
			if t.Key().Kind() != reflect.String {
				needMap[t] = true
			}
			return visit(t.Elem())
		}
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	opts := &gjschema.ForOptions{TypeSchemas: ts}
	if err := buildMapSchemas(needMap, opts); err != nil {
		return nil, err
	}
	return opts, nil
}

// buildMapSchemas resolves map overrides bottom up so an element schema never
// asks for a map whose own override is still missing.
func buildMapSchemas(pending map[reflect.Type]bool, opts *gjschema.ForOptions) error {
	for len(pending) > 0 {
		progress := false
		for mt := range pending {
			if subtreeHasPendingMap(mt.Elem(), pending) {
				continue
			}
			elem, err := gjschema.ForType(mt.Elem(), opts)
			if err != nil {
				return invalidArg(err.Error(), nil)
			}
			opts.TypeSchemas[mt] = &gjschema.Schema{
				Types:                []string{"null", "object"},
				AdditionalProperties: elem,
			}
			delete(pending, mt)
			progress = true
		}
		if !progress {
			return invalidArg("cycle in map types", nil)
		}
	}
	return nil
}

func subtreeHasPendingMap(t reflect.Type, pending map[reflect.Type]bool) bool {
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type) bool
	walk = func(t reflect.Type) bool {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if seen[t] {
			return false
		}
		seen[t] = true
		if pending[t] {
			return true
		}
		switch t.Kind() {
		case reflect.Struct:
			for _, f := range reflect.VisibleFields(t) {
				if !f.IsExported() && (!f.Anonymous || !embedStructKind(f.Type)) {
					continue
				}
				if walk(f.Type) {
					return true
				}
			}
		case reflect.Slice, reflect.Array:
			return walk(t.Elem())
		case reflect.Map:
			return walk(t.Elem())
		}
		return false
	}
	return walk(t)
}

// patchGeneratedSchema fixes the parts of the generated tree the library and
// the shape-based null patch cannot know: the string tag option, embedded
// non-struct fields and pointers whose type has its own encoding.
func patchGeneratedSchema(raw json.RawMessage, t reflect.Type, opts *gjschema.ForOptions) (json.RawMessage, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw, nil
	}
	if err := patchSchemaNode(doc, t, opts); err != nil {
		return nil, err
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, invalidArgf("marshal generated schema: %v", err)
	}
	return out, nil
}

func patchSchemaNode(node map[string]any, t reflect.Type, opts *gjschema.ForOptions) error {
	for t.Kind() == reflect.Pointer {
		ensureNullAdmitted(node)
		t = t.Elem()
	}
	if typeEncoding(t) != ruleNone {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		return patchObjectNode(node, t, opts)
	case reflect.Slice, reflect.Array:
		if items, ok := node["items"].(map[string]any); ok {
			return patchSchemaNode(items, t.Elem(), opts)
		}
	case reflect.Map:
		if ap, ok := node["additionalProperties"].(map[string]any); ok {
			return patchSchemaNode(ap, t.Elem(), opts)
		}
	}
	return nil
}

func patchObjectNode(node map[string]any, t reflect.Type, opts *gjschema.ForOptions) error {
	props, _ := node["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		node["properties"] = props
	}
	required, _ := node["required"].([]any)
	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() {
			continue
		}
		name := jsonName(f)
		if name == "" {
			continue
		}
		// encoding/json flattens an embedded struct or pointer to one; its
		// promoted fields are in VisibleFields and get their own properties.
		if f.Anonymous && embedStructKind(f.Type) {
			continue
		}
		settings := jsonFieldSettings(f)
		if _, ok := props[name]; !ok {
			// Only an anonymous non-struct field is still missing by design;
			// any other absent property was a shape decision made above.
			if !f.Anonymous {
				continue
			}
			// A struct that embeds json.RawMessage promotes its MarshalJSON and
			// writes any JSON value, so it has no member for the embedded field.
			if rawMessageFieldType(f.Type) {
				continue
			}
			// The library skips anonymous fields, but encoding/json writes an
			// embedded non-struct value as a member named after the field.
			sub, err := generateSubSchema(f.Type, opts)
			if err != nil {
				return err
			}
			props[name] = sub
			if !settings["omitempty"] && !settings["omitzero"] {
				required = appendUnique(required, name)
			}
		}
		if schema, ok := props[name].(map[string]any); ok {
			if replacement := stringOptionSchema(f.Type, settings); replacement != nil {
				props[name] = replacement
				continue
			}
			if err := patchSchemaNode(schema, f.Type, opts); err != nil {
				return err
			}
		}
	}
	if len(required) > 0 {
		node["required"] = required
	}
	return nil
}

func generateSubSchema(t reflect.Type, opts *gjschema.ForOptions) (any, error) {
	s, err := gjschema.ForType(t, opts)
	if err != nil {
		return nil, invalidArg(err.Error(), nil)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, invalidArgf("marshal generated schema: %v", err)
	}
	raw = patchNullTypesInGeneratedSchema(raw)
	var node map[string]any
	if err := json.Unmarshal(raw, &node); err != nil {
		return nil, invalidArgf("parse generated schema: %v", err)
	}
	if err := patchSchemaNode(node, t, opts); err != nil {
		return nil, err
	}
	return node, nil
}

func jsonFieldSettings(f reflect.StructField) map[string]bool {
	_, settings, _ := parseTag(f.Tag.Get("json"))
	return settings
}

func appendUnique(list []any, value string) []any {
	for _, item := range list {
		if item == value {
			return list
		}
	}
	return append(list, value)
}

func ensureNullAdmitted(node map[string]any) {
	switch typ := node["type"].(type) {
	case string:
		node["type"] = []any{"null", typ}
		return
	case []any:
		for _, item := range typ {
			if item == "null" {
				return
			}
		}
		node["type"] = append(typ, "null")
		return
	}
	if types, ok := node["types"].([]any); ok {
		for _, item := range types {
			if item == "null" {
				return
			}
		}
		node["types"] = append(types, "null")
	}
	// A schema without a type keyword already admits null.
}

// The ,string option patterns of the design: the literals encoding/json
// writes, after following pointers.
const (
	signedIntPattern    = `^-?(0|[1-9][0-9]*)$`
	unsignedIntPattern  = `^(0|[1-9][0-9]*)$`
	floatNumberPattern  = `^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`
	booleanPattern      = `^(true|false)$`
	quotedStringPattern = `^"([^"\\]|\\.)*"$`
)

// stringOptionSchema returns the property schema for a field tagged ,string,
// or nil when the type keeps its own encoding or has no string form.
func stringOptionSchema(t reflect.Type, settings map[string]bool) map[string]any {
	if !settings["string"] {
		return nil
	}
	// encoding/json follows one unnamed pointer before reading the option and
	// applies it only when the result is a supported kind, so a named pointer
	// type keeps its ordinary pointer encoding, and so does a pointer to a
	// pointer.
	base := t
	if base.Kind() == reflect.Pointer {
		if base.Name() != "" {
			return nil
		}
		base = base.Elem()
		if base.Kind() == reflect.Pointer {
			return nil
		}
	}
	switch typeEncoding(t) {
	case ruleNone, ruleNumber:
	default:
		return nil
	}
	nullable := t.Kind() == reflect.Pointer
	pattern := stringOptionPattern(base)
	if pattern == "" {
		return nil
	}
	node := map[string]any{"pattern": pattern}
	if nullable {
		node["type"] = []any{"null", "string"}
	} else {
		node["type"] = "string"
	}
	return node
}

func stringOptionPattern(t reflect.Type) string {
	if t == numberReflectType {
		return floatNumberPattern
	}
	switch t.Kind() {
	case reflect.String:
		return quotedStringPattern
	case reflect.Bool:
		return booleanPattern
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return signedIntPattern
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return unsignedIntPattern
	case reflect.Float32, reflect.Float64:
		return floatNumberPattern
	}
	return ""
}
