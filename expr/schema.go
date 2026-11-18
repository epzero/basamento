package expr

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"cel.dev/cel-go/cel"
)

type schemaType struct {
	schema   map[string]any
	defs     map[string]map[string]any
	typeName string // struct object type name when applicable
	rootDef  string // $defs entry name when the variable schema is a local $ref
	celType  *cel.Type
}

func parseSchema(raw json.RawMessage, varName string) (*schemaType, error) {
	var doc any
	if len(raw) == 0 {
		return &schemaType{celType: cel.DynType}, nil
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("invalid schema JSON: %w", err)
	}
	st := &schemaType{}
	switch v := doc.(type) {
	case bool:
		if !v {
			return nil, fmt.Errorf("invalid schema")
		}
		return &schemaType{celType: cel.DynType}, nil
	case map[string]any:
		st.schema = v
		if defs, ok := v["$defs"].(map[string]any); ok {
			st.defs = make(map[string]map[string]any, len(defs))
			for k, d := range defs {
				if dm, ok := d.(map[string]any); ok {
					st.defs[k] = dm
				}
			}
		}
	default:
		return &schemaType{celType: cel.DynType}, nil
	}
	if ref, ok := st.schema["$ref"].(string); ok {
		return st.resolveRef(ref, varName)
	}
	t, objName, err := st.mapSchema(st.schema, varName)
	if err != nil {
		return nil, err
	}
	st.celType = t
	st.typeName = objName
	return st, nil
}

func (st *schemaType) resolveRef(ref, varName string) (*schemaType, error) {
	if !strings.HasPrefix(ref, "#/$defs/") {
		return nil, fmt.Errorf("external reference %q", ref)
	}
	name := strings.TrimPrefix(ref, "#/$defs/")
	def, ok := st.defs[name]
	if !ok {
		return nil, fmt.Errorf("missing definition for reference %q", ref)
	}
	child := &schemaType{schema: def, defs: st.defs}
	t, objName, err := child.mapSchema(def, varName+"."+name)
	if err != nil {
		return nil, err
	}
	child.celType = t
	child.typeName = objName
	child.rootDef = name
	return child, nil
}

func (st *schemaType) mapSchema(s map[string]any, path string) (*cel.Type, string, error) {
	return st.mapSchemaVisit(s, path, nil)
}

func (st *schemaType) mapSchemaVisit(s map[string]any, path string, visiting map[string]bool) (*cel.Type, string, error) {
	if visiting == nil {
		visiting = map[string]bool{}
	}
	if ref, ok := s["$ref"].(string); ok {
		if strings.HasPrefix(ref, "#/$defs/") {
			name := strings.TrimPrefix(ref, "#/$defs/")
			if visiting[name] {
				return cel.DynType, "", nil
			}
			def, ok := st.defs[name]
			if !ok {
				return nil, "", fmt.Errorf("missing definition for reference %q", ref)
			}
			visiting[name] = true
			defer delete(visiting, name)
			return st.mapSchemaVisit(def, path, visiting)
		}
		return nil, "", fmt.Errorf("external reference %q", ref)
	}
	if t, ok := s["type"]; ok {
		switch tv := t.(type) {
		case string:
			return st.primitiveOrCompositeVisit(tv, s, path, visiting)
		case []any:
			return cel.DynType, "", nil
		}
	}
	if _, ok := s["enum"]; ok {
		return st.enumType(s)
	}
	if _, ok := s["anyOf"]; ok {
		return cel.DynType, "", nil
	}
	if _, ok := s["oneOf"]; ok {
		return cel.DynType, "", nil
	}
	if _, ok := s["allOf"]; ok {
		return cel.DynType, "", nil
	}
	if c, ok := s["const"]; ok {
		return st.constType(c)
	}
	if len(s) == 0 {
		return cel.DynType, "", nil
	}
	return cel.DynType, "", nil
}

func (st *schemaType) primitiveOrCompositeVisit(t string, s map[string]any, path string, visiting map[string]bool) (*cel.Type, string, error) {
	switch t {
	case "boolean":
		return cel.BoolType, "", nil
	case "integer":
		return cel.IntType, "", nil
	case "number":
		return cel.DoubleType, "", nil
	case "string":
		if fmt, _ := s["format"].(string); fmt == "date-time" {
			return cel.TimestampType, "", nil
		}
		return cel.StringType, "", nil
	case "null":
		return cel.NullType, "", nil
	case "array":
		if items, ok := s["items"]; ok {
			itemSchema, ok := items.(map[string]any)
			if !ok {
				return cel.ListType(cel.DynType), "", nil
			}
			elem, objName, err := st.mapSchemaVisit(itemSchema, path+"[]", visiting)
			if err != nil {
				return nil, "", err
			}
			return cel.ListType(elem), objName, nil
		}
		return cel.ListType(cel.DynType), "", nil
	case "object":
		return st.objectType(s, path)
	default:
		return cel.DynType, "", nil
	}
}

func (st *schemaType) objectType(s map[string]any, path string) (*cel.Type, string, error) {
	_, hasProps := s["properties"].(map[string]any)
	_, hasAP := s["additionalProperties"]
	if hasProps {
		typeName := "expr.obj." + sanitizeTypeName(path)
		return cel.ObjectType(typeName), typeName, nil
	}
	if hasAP {
		ap := s["additionalProperties"]
		switch apv := ap.(type) {
		case map[string]any:
			elem, _, err := st.mapSchemaVisit(apv, path+"*", nil)
			if err != nil {
				return nil, "", err
			}
			return cel.MapType(cel.StringType, elem), "", nil
		case bool:
			if !apv {
				return cel.MapType(cel.StringType, cel.DynType), "", nil
			}
		}
	}
	return cel.MapType(cel.StringType, cel.DynType), "", nil
}

func (st *schemaType) enumType(s map[string]any) (*cel.Type, string, error) {
	vals, ok := s["enum"].([]any)
	if !ok || len(vals) == 0 {
		return cel.DynType, "", nil
	}
	first := jsonTypeOf(vals[0])
	for _, v := range vals[1:] {
		if jsonTypeOf(v) != first {
			return cel.DynType, "", nil
		}
	}
	switch first {
	case "boolean":
		return cel.BoolType, "", nil
	case "integer":
		return cel.IntType, "", nil
	case "number":
		return cel.DoubleType, "", nil
	case "string":
		return cel.StringType, "", nil
	case "null":
		return cel.NullType, "", nil
	default:
		return cel.DynType, "", nil
	}
}

func (st *schemaType) constType(c any) (*cel.Type, string, error) {
	switch jsonTypeOf(c) {
	case "boolean":
		return cel.BoolType, "", nil
	case "integer":
		return cel.IntType, "", nil
	case "number":
		return cel.DoubleType, "", nil
	case "string":
		return cel.StringType, "", nil
	case "null":
		return cel.NullType, "", nil
	default:
		return cel.DynType, "", nil
	}
}

func jsonTypeOf(v any) string {
	switch f := v.(type) {
	case bool:
		return "boolean"
	case float64:
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return "number"
		}
		if float64IntegralOutsideInt64(f) {
			return "integer"
		}
		if _, ok := float64IntegralInt64(f); ok {
			return "integer"
		}
		return "number"
	case string:
		return "string"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}

func sanitizeTypeName(path string) string {
	var b strings.Builder
	for _, r := range path {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
			b.WriteString(strconv.FormatInt(int64(r), 16))
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "root"
	}
	return b.String()
}

func formatCELType(t *cel.Type) string {
	if t == nil {
		return ""
	}
	return t.String()
}

func assignableResult(expected *schemaType, actual *cel.Type) error {
	if expected == nil || expected.celType == nil {
		return nil
	}
	exp := expected.celType
	if !exp.IsAssignableType(actual) {
		// int assignable to double schema
		if exp.Kind() == cel.DoubleKind && actual.IsAssignableType(cel.IntType) {
			return nil
		}
		return fmt.Errorf("expected type %q, actual type %q", formatCELType(exp), formatCELType(actual))
	}
	return nil
}
