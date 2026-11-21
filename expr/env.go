package expr

import (
	"fmt"
	"strings"

	_ "time/tzdata"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/ext"
)

// EnvOption configures an expression environment.
type EnvOption func(*Env) error

// WithCostLimit sets the per-evaluation cost limit (default 1_000_000).
func WithCostLimit(limit uint64) EnvOption {
	return func(e *Env) error {
		e.costLimit = limit
		return nil
	}
}

// Env is a type-checked CEL environment with deterministic evaluation rules.
type Env struct {
	vars      []Var
	schemas   map[string]*schemaType
	objects   map[string]*jsonObjectType
	celEnv    *cel.Env
	registry  *types.Registry
	adapter   *sortedAdapter
	costLimit uint64
}

// NewEnv builds an expression environment for the declared variables.
func NewEnv(vars []Var, opts ...EnvOption) (*Env, error) {
	e := &Env{
		vars:      vars,
		schemas:   make(map[string]*schemaType, len(vars)),
		objects:   make(map[string]*jsonObjectType),
		costLimit: defaultCostLimit,
	}
	for _, o := range opts {
		if err := o(e); err != nil {
			return nil, err
		}
	}
	reg, err := types.NewRegistry()
	if err != nil {
		return nil, err
	}
	adapter := newSortedAdapter(reg)
	e.adapter = adapter
	e.registry = reg
	celOpts := []cel.EnvOption{
		cel.CustomTypeAdapter(adapter),
		cel.CustomTypeProvider(newSortedTypeProvider(reg)),
		cel.Lib(deterministicTimestampLib{}),
		cel.OptionalTypes(),
		ext.Strings(ext.StringsLocale("en_US")),
		ext.Strings(),
		ext.Math(),
		ext.Lists(),
		ext.Sets(),
		ext.Encoders(),
		ext.Bindings(),
	}
	for _, v := range vars {
		st, err := parseSchema(v.Schema, v.Name)
		if err != nil {
			return nil, exprErr(fmt.Sprintf("variable %q: %s", v.Name, err.Error()), map[string]any{"variable": v.Name})
		}
		e.schemas[v.Name] = st
		if st.typeName != "" {
			if _, err := e.objectFromSchema(st, adapter); err != nil {
				return nil, err
			}
		} else if st.schema != nil {
			// A variable whose root is a map still needs the object types
			// nested in it: a map's values keep the fields of their
			// properties (design.md §2, EXPR-003-05).
			visiting := map[string]bool{}
			if st.rootDef != "" {
				visiting[st.rootDef] = true
			}
			if err := e.registerNestedObjects(st.schema, st.path, st, adapter, visiting); err != nil {
				return nil, exprErr(fmt.Sprintf("schema %q: %s", v.Name, err.Error()), map[string]any{"variable": v.Name})
			}
		}
		celOpts = append(celOpts, cel.Variable(v.Name, st.celType))
	}
	celEnv, err := cel.NewEnv(celOpts...)
	if err != nil {
		return nil, err
	}
	e.celEnv = celEnv
	return e, nil
}

// objectFromSchema registers the object type a variable's schema declares. The
// schema document is normalized first: parseSchema names the element type of a
// root array, but st.schema still holds the array, so building from it would
// register an empty object type (design.md §2). objectSchemaForBuild follows
// that array's items, or a $ref chain, to the document with the properties and
// reports the schema path it reached, which names the type as mapSchema named
// it.
func (e *Env) objectFromSchema(st *schemaType, adapter *sortedAdapter) (*jsonObjectType, error) {
	doc, defName, path, err := objectSchemaForBuild(st.schema, st, st.path)
	if err != nil {
		return nil, exprErr(fmt.Sprintf("schema %q: %s", st.typeName, err.Error()), map[string]any{"variable": st.typeName})
	}
	visiting := map[string]bool{}
	if defName == "" {
		defName = st.rootDef
	}
	if defName != "" {
		visiting[defName] = true
	}
	return e.buildObjectType(doc, path, st, adapter, visiting)
}

// buildObjectType registers the object type for the object schema doc, which
// sits at the schema path path: the type is named the way mapSchemaVisit names
// it, so a field typed with that name finds this type, and its own fields take
// the path below it. visiting holds the $defs names that are being built by an
// enclosing call: a reference back to one of them maps to dyn at that point
// (design.md §2), so recursive definitions terminate instead of expanding
// without end.
func (e *Env) buildObjectType(doc map[string]any, path string, st *schemaType, adapter *sortedAdapter, visiting map[string]bool) (*jsonObjectType, error) {
	typeName := objectTypeNameForSchemaPath(path)
	if obj := e.objects[typeName]; obj != nil {
		return obj, nil
	}
	props, _ := doc["properties"].(map[string]any)
	reqSet := map[string]bool{}
	if req, ok := doc["required"].([]any); ok {
		for _, r := range req {
			if s, ok := r.(string); ok {
				reqSet[s] = true
			}
		}
	}
	fields := make(map[string]fieldMeta)
	for _, pname := range sortedStringKeys(props) {
		pm, ok := props[pname].(map[string]any)
		if !ok {
			continue
		}
		fieldPath := joinSchemaPath(path, pname)
		t, _, err := st.mapSchemaVisit(pm, fieldPath, visiting)
		if err != nil {
			return nil, exprErr(fmt.Sprintf("schema %q: %s", typeName+"."+pname, err.Error()), map[string]any{"variable": typeName})
		}
		fields[pname] = fieldMeta{celType: t, required: reqSet[pname]}
		if err := e.registerNestedObjects(pm, fieldPath, st, adapter, visiting); err != nil {
			return nil, exprErr(fmt.Sprintf("schema %q: %s", typeName+"."+pname, err.Error()), map[string]any{"variable": typeName})
		}
	}
	obj := &jsonObjectType{typeName: typeName, fields: fields, adapter: adapter, objects: e.objects}
	e.objects[typeName] = obj
	if err := e.registry.RegisterType(obj); err != nil {
		delete(e.objects, typeName)
		return nil, exprErr(fmt.Sprintf("register object type %q: %s", typeName, err.Error()), map[string]any{"variable": typeName})
	}
	return obj, nil
}

// registerNestedObjects registers the object types nested in a schema value:
// the fields of an object, the element type of a list's items and the value
// type of a map's additionalProperties, at any depth. An object type keeps the
// fields of its properties wherever it appears (design.md §2, EXPR-003-05), so
// an object reached only as a map value must be registered before an expression
// types and reads one of its fields.
func (e *Env) registerNestedObjects(doc map[string]any, path string, st *schemaType, adapter *sortedAdapter, visiting map[string]bool) error {
	if ref, ok := doc["$ref"].(string); ok {
		// One hop at a time on the shared set, deleting each name on return as
		// mapSchemaVisit does: N aliases cost N hops, and a definition that
		// refers back to itself stops here (design.md §2). parseSchema has
		// validated every reference reachable from here (schema.go), so it is
		// local and names an existing definition.
		name := strings.TrimPrefix(ref, "#/$defs/")
		if visiting[name] {
			return nil
		}
		visiting[name] = true
		defer delete(visiting, name)
		return e.registerNestedObjects(st.defs[name], path, st, adapter, visiting)
	}
	switch typ, _ := doc["type"].(string); typ {
	case "object":
		if _, ok := doc["properties"].(map[string]any); ok {
			// buildObjectType walks these properties itself and registers the
			// objects nested in them under the names mapSchemaVisit computes,
			// so walking them here too would repeat that work at every level.
			_, err := e.buildObjectType(doc, path, st, adapter, visiting)
			return err
		}
		if ap, ok := doc["additionalProperties"].(map[string]any); ok {
			return e.registerNestedObjects(ap, joinSchemaPathMapValue(path), st, adapter, visiting)
		}
	case "array":
		if items, ok := doc["items"].(map[string]any); ok {
			return e.registerNestedObjects(items, joinSchemaPathArray(path), st, adapter, visiting)
		}
	}
	return nil
}

// objectSchemaForBuild follows $ref chains and array items to the schema
// document that declares an object type, and returns the $defs name it came
// from so callers can track recursion, along with the schema path the document
// sits at: a $ref keeps the path of the place it is written and each array hop
// adds "[]", as mapSchemaVisit names them.
func objectSchemaForBuild(pm map[string]any, st *schemaType, path string) (map[string]any, string, string, error) {
	doc, defName := pm, ""
	for {
		if ref, ok := doc["$ref"].(string); ok {
			// The chain is acyclic: a chain that closed on itself types as dyn
			// in mapSchemaVisit, so the schema never resolves to an object and
			// this function is not called for it (design.md §2).
			resolved, names, _, err := defRefChain(ref, st.defs)
			if err != nil {
				return nil, "", "", err
			}
			doc, defName = resolved, names[len(names)-1]
			continue
		}
		if typ, _ := doc["type"].(string); typ == "array" {
			items, ok := doc["items"].(map[string]any)
			if !ok {
				return doc, defName, path, nil
			}
			doc = items
			path = joinSchemaPathArray(path)
			continue
		}
		return doc, defName, path, nil
	}
}
