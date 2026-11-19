package expr

import (
	"fmt"

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
// that array's items, or a $ref chain, to the document with the properties.
func (e *Env) objectFromSchema(st *schemaType, adapter *sortedAdapter) (*jsonObjectType, error) {
	doc, defName, err := objectSchemaForBuild(st.schema, st)
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
	return e.buildObjectType(doc, st.typeName, st, adapter, visiting)
}

// buildObjectType registers the object type for doc. visiting holds the $defs
// names that are being built by an enclosing call: a reference back to one of
// them maps to dyn at that point (design.md §2), so recursive definitions
// terminate instead of expanding without end.
func (e *Env) buildObjectType(doc map[string]any, typeName string, st *schemaType, adapter *sortedAdapter, visiting map[string]bool) (*jsonObjectType, error) {
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
		t, objName, err := st.mapSchemaVisit(pm, typeName+"."+pname, visiting)
		if err != nil {
			return nil, exprErr(fmt.Sprintf("schema %q: %s", typeName+"."+pname, err.Error()), map[string]any{"variable": typeName})
		}
		fields[pname] = fieldMeta{celType: t, required: reqSet[pname]}
		if objName != "" {
			objSchema, defName, err := objectSchemaForBuild(pm, st)
			if err != nil {
				return nil, exprErr(err.Error(), map[string]any{"variable": typeName})
			}
			if defName != "" && visiting[defName] {
				continue
			}
			next := visiting
			if defName != "" {
				next = cloneVisiting(visiting)
				next[defName] = true
			}
			if _, err := e.buildObjectType(objSchema, objName, st, adapter, next); err != nil {
				return nil, exprErr(fmt.Sprintf("schema %q: %s", objName, err.Error()), map[string]any{"variable": typeName})
			}
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

func cloneVisiting(visiting map[string]bool) map[string]bool {
	out := make(map[string]bool, len(visiting)+1)
	for name := range visiting {
		out[name] = true
	}
	return out
}

// objectSchemaForBuild follows $ref chains and array items to the schema
// document that declares an object type, and returns the $defs name it came
// from so callers can track recursion.
func objectSchemaForBuild(pm map[string]any, st *schemaType) (map[string]any, string, error) {
	seen := map[string]bool{}
	doc, defName := pm, ""
	for {
		if ref, ok := doc["$ref"].(string); ok {
			resolved, names, err := defRefChain(ref, st.defs)
			if err != nil {
				return nil, "", err
			}
			for _, name := range names {
				if seen[name] {
					return nil, "", fmt.Errorf("cyclic reference %q", ref)
				}
				seen[name] = true
			}
			doc, defName = resolved, names[len(names)-1]
			continue
		}
		if typ, _ := doc["type"].(string); typ == "array" {
			items, ok := doc["items"].(map[string]any)
			if !ok {
				return doc, defName, nil
			}
			doc = items
			continue
		}
		return doc, defName, nil
	}
}
