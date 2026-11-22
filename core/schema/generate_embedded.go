package schema

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
)

// patchEmbeddedJSONFields aligns generated struct object schemas with encoding/json
// for anonymous embedded fields that google/jsonschema-go does not model.
func patchEmbeddedJSONFields(raw json.RawMessage, root reflect.Type, opts *genOptions) (json.RawMessage, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw, nil
	}
	t := root
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		if err := patchStructObject(doc, t, opts); err != nil {
			return nil, err
		}
	case reflect.Slice:
		if items, ok := doc["items"].(map[string]any); ok {
			if err := patchElementSchema(items, t.Elem(), opts); err != nil {
				return nil, err
			}
		}
	case reflect.Map:
		if t.Key().Kind() == reflect.String {
			if ap, ok := doc["additionalProperties"].(map[string]any); ok {
				if err := patchElementSchema(ap, t.Elem(), opts); err != nil {
					return nil, err
				}
			}
		}
	default:
		return raw, nil
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return raw, err
	}
	return out, nil
}

func patchStructObject(obj map[string]any, t reflect.Type, opts *genOptions) error {
	if structEmbedsRawMessage(t) {
		clearEmbeddedRawMessageObjectSchema(obj)
		return nil
	}
	// A struct with its own encoding describes a string or any JSON value; its
	// fields are not properties of that schema (REQ-SCHEMA-041, 042, 045).
	if typeEncoding(t) != ruleNone {
		return nil
	}
	props, _ := obj["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		obj["properties"] = props
	}
	required := requiredStrings(obj["required"])

	for _, f := range reflect.VisibleFields(t) {
		if !anonymousEmbedJSONRelevant(f) {
			continue
		}
		ft := f.Type

		if ft.Kind() == reflect.Pointer && ft.Elem().Kind() == reflect.Struct && embedPromotes(f) {
			elem := ft.Elem()
			if elem != rawMessageReflectType {
				removeRequired(&required, jsonFieldNames(elem)...)
				continue
			}
		}

		if embedStructKind(ft) {
			inner := structFieldType(ft)
			if inner == rawMessageReflectType {
				continue
			}
			name := jsonName(f)
			if name == "" {
				removePropertiesAndRequired(props, &required, jsonFieldNames(inner)...)
				continue
			}
			if !embedPromotes(f) {
				removePropertiesAndRequired(props, &required, jsonFieldNames(inner)...)
				sub, err := typeSchemaMap(ft, opts)
				if err != nil {
					return err
				}
				props[name] = sub
				if !embedOmittable(f) {
					required = appendIfMissing(required, name)
				}
			}
			continue
		}

		if ft.Kind() == reflect.Interface {
			key := jsonName(f)
			if key != "" {
				props[key] = map[string]any{}
			}
			continue
		}

		if rawMessageFieldType(ft) {
			continue
		}

		key := jsonName(f)
		if key == "" {
			continue
		}
		sub, err := typeSchemaMap(ft, opts)
		if err != nil {
			return err
		}
		props[key] = sub
		if !embedOmittable(f) {
			required = appendIfMissing(required, key)
		}
	}

	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() || f.Anonymous {
			continue
		}
		key := jsonName(f)
		if key == "" || rawMessageFieldType(f.Type) {
			continue
		}
		elem := f.Type
		for elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		switch elem.Kind() {
		case reflect.Struct, reflect.Slice, reflect.Map:
			sub, err := typeSchemaMap(f.Type, opts)
			if err != nil {
				return err
			}
			props[key] = sub
			if !embedOmittable(f) {
				required = appendIfMissing(required, key)
			}
		}
	}

	setRequired(obj, required)

	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() || !f.Anonymous || rawMessageFieldType(f.Type) {
			continue
		}
		name := jsonName(f)
		if name == "" {
			continue
		}
		sub, ok := props[name].(map[string]any)
		if !ok {
			continue
		}
		ft := structFieldType(f.Type)
		if ft.Kind() == reflect.Struct {
			if err := patchStructObject(sub, ft, opts); err != nil {
				return err
			}
		}
	}
	return nil
}

func typeSchemaMap(t reflect.Type, opts *genOptions) (map[string]any, error) {
	s, err := librarySchema(t, opts)
	if err != nil {
		return nil, invalidArg(err.Error(), nil)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, invalidArgf("marshal subschema: %v", err)
	}
	m, err := schemaMapFromMarshaled(t, raw)
	if err != nil {
		return nil, err
	}
	patchSchemaValue(m, false)
	inner := structFieldType(t)
	switch inner.Kind() {
	case reflect.Struct:
		if err := patchStructObject(m, inner, opts); err != nil {
			return nil, err
		}
	case reflect.Slice:
		if items, ok := m["items"].(map[string]any); ok {
			if err := patchElementSchema(items, inner.Elem(), opts); err != nil {
				return nil, err
			}
		}
	case reflect.Map:
		if inner.Key().Kind() == reflect.String {
			if ap, ok := m["additionalProperties"].(map[string]any); ok {
				if err := patchElementSchema(ap, inner.Elem(), opts); err != nil {
					return nil, err
				}
			}
		}
	}
	return m, nil
}

func schemaMapFromMarshaled(t reflect.Type, raw []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		return m, nil
	}
	// An unconstrained schema marshals as the boolean schema true.
	switch strings.TrimSpace(string(raw)) {
	case "true":
		return map[string]any{}, nil
	case "false":
		return map[string]any{"not": map[string]any{}}, nil
	}
	if rawMessageFieldType(t) {
		return map[string]any{}, nil
	}
	return nil, invalidArgf("unmarshal subschema: invalid schema %s", raw)
}

func patchElementSchema(node map[string]any, elem reflect.Type, opts *genOptions) error {
	elem = structFieldType(elem)
	if elem.Kind() != reflect.Struct {
		return nil
	}
	return patchStructObject(node, elem, opts)
}

// anonymousEmbedJSONRelevant is true when encoding/json considers an anonymous field.
func anonymousEmbedJSONRelevant(f reflect.StructField) bool {
	if !f.Anonymous {
		return false
	}
	if f.IsExported() {
		return true
	}
	return embedStructKind(f.Type)
}

func embedStructKind(ft reflect.Type) bool {
	if ft.Kind() == reflect.Struct {
		return true
	}
	return ft.Kind() == reflect.Pointer && ft.Elem().Kind() == reflect.Struct
}

func structFieldType(ft reflect.Type) reflect.Type {
	for ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	return ft
}

func rawMessageFieldType(ft reflect.Type) bool {
	if ft == rawMessageReflectType {
		return true
	}
	return ft.Kind() == reflect.Pointer && ft.Elem() == rawMessageReflectType
}

func embedPromotes(f reflect.StructField) bool {
	if !f.Anonymous {
		return false
	}
	ft := structFieldType(f.Type)
	if ft.Kind() != reflect.Struct {
		return false
	}
	tag := f.Tag.Get("json")
	if tag == "" {
		return true
	}
	if tag == "-" {
		return false
	}
	name, _, _ := parseTag(tag)
	return name == ""
}

// embedOmittable reports whether the field's tag lets encoding/json leave the
// member out, so the property must not be required (REQ-SCHEMA-003).
func embedOmittable(f reflect.StructField) bool {
	settings := jsonFieldSettings(f)
	return settings["omitempty"] || settings["omitzero"]
}

func structEmbedsRawMessage(t reflect.Type) bool {
	for _, f := range reflect.VisibleFields(t) {
		if !anonymousEmbedJSONRelevant(f) {
			continue
		}
		if rawMessageFieldType(f.Type) {
			return true
		}
	}
	return false
}

// clearEmbeddedRawMessageObjectSchema replaces a struct schema with an unconstrained
// schema so every JSON value encoding/json may emit via promoted MarshalJSON validates.
func clearEmbeddedRawMessageObjectSchema(obj map[string]any) {
	for k := range obj {
		delete(obj, k)
	}
}

func jsonFieldNames(t reflect.Type) []string {
	t = structFieldType(t)
	if t.Kind() != reflect.Struct {
		return nil
	}
	var names []string
	for _, f := range reflect.VisibleFields(t) {
		if f.Anonymous {
			if embedPromotes(f) {
				names = append(names, jsonFieldNames(f.Type)...)
			}
			continue
		}
		if !f.IsExported() {
			continue
		}
		if n := jsonName(f); n != "" {
			names = append(names, n)
		}
	}
	return names
}

func requiredStrings(v any) []string {
	var required []string
	switch r := v.(type) {
	case []any:
		for _, item := range r {
			if s, ok := item.(string); ok {
				required = append(required, s)
			}
		}
	case []string:
		required = append(required, r...)
	}
	return required
}

func setRequired(obj map[string]any, required []string) {
	if len(required) == 0 {
		delete(obj, "required")
		return
	}
	out := make([]any, len(required))
	for i, s := range required {
		out[i] = s
	}
	obj["required"] = out
}

func removeRequired(required *[]string, keys ...string) {
	for _, k := range keys {
		*required = slices.DeleteFunc(*required, func(s string) bool { return s == k })
	}
}

func removePropertiesAndRequired(props map[string]any, required *[]string, keys ...string) {
	for _, k := range keys {
		delete(props, k)
		*required = slices.DeleteFunc(*required, func(s string) bool { return s == k })
	}
}

func appendIfMissing(required []string, key string) []string {
	if slices.Contains(required, key) {
		return required
	}
	return append(required, key)
}
