package schema

import (
	"encoding/json"
	"reflect"
	"slices"

	gjschema "github.com/google/jsonschema-go/jsonschema"
)

// patchEmbeddedJSONFields aligns generated struct object schemas with encoding/json
// for anonymous embedded fields that google/jsonschema-go does not model.
func patchEmbeddedJSONFields(raw json.RawMessage, root reflect.Type, opts *gjschema.ForOptions) (json.RawMessage, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw, nil
	}
	t := root
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return raw, nil
	}
	if err := patchStructObject(doc, t, opts); err != nil {
		return nil, err
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return raw, err
	}
	return out, nil
}

func patchStructObject(obj map[string]any, t reflect.Type, opts *gjschema.ForOptions) error {
	props, _ := obj["properties"].(map[string]any)
	if props == nil {
		props = map[string]any{}
		obj["properties"] = props
	}
	required := requiredStrings(obj["required"])

	if structEmbedsRawMessage(t) {
		// encoding/json inlines RawMessage and may emit null for a zero value.
		obj["type"] = []any{"null", "object"}
		obj["additionalProperties"] = true
		required = nil
	}

	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() || !f.Anonymous {
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
				required = appendIfMissing(required, name)
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

		if ft == rawMessageReflectType {
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
		required = appendIfMissing(required, key)
	}

	setRequired(obj, required)

	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() || f.Anonymous {
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

func typeSchemaMap(t reflect.Type, opts *gjschema.ForOptions) (map[string]any, error) {
	s, err := gjschema.ForType(t, opts)
	if err != nil {
		return nil, invalidArg(err.Error(), nil)
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, invalidArgf("marshal subschema: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, invalidArgf("unmarshal subschema: %v", err)
	}
	patchSchemaValue(m, false)
	inner := structFieldType(t)
	if inner.Kind() == reflect.Struct {
		if err := patchStructObject(m, inner, opts); err != nil {
			return nil, err
		}
	}
	return m, nil
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

func structEmbedsRawMessage(t reflect.Type) bool {
	for _, f := range reflect.VisibleFields(t) {
		if f.Anonymous && f.IsExported() && f.Type == rawMessageReflectType {
			return true
		}
	}
	return false
}

func jsonFieldNames(t reflect.Type) []string {
	t = structFieldType(t)
	if t.Kind() != reflect.Struct {
		return nil
	}
	var names []string
	for _, f := range reflect.VisibleFields(t) {
		if !f.IsExported() {
			continue
		}
		if f.Anonymous {
			if embedPromotes(f) {
				names = append(names, jsonFieldNames(f.Type)...)
			}
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
