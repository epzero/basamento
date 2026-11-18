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

func (e *Env) objectFromSchema(st *schemaType, adapter *sortedAdapter) (*jsonObjectType, error) {
	return e.buildObjectType(st.schema, st.typeName, st, adapter)
}

func (e *Env) buildObjectType(doc map[string]any, typeName string, st *schemaType, adapter *sortedAdapter) (*jsonObjectType, error) {
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
	visiting := map[string]bool{}
	if st != nil && st.rootDef != "" {
		visiting[st.rootDef] = true
	}
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
			objSchema, err := objectSchemaForBuild(pm, st)
			if err != nil {
				return nil, exprErr(err.Error(), map[string]any{"variable": typeName})
			}
			if _, err := e.buildObjectType(objSchema, objName, st, adapter); err != nil {
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

func objectSchemaForBuild(pm map[string]any, st *schemaType) (map[string]any, error) {
	if ref, ok := pm["$ref"].(string); ok {
		if !strings.HasPrefix(ref, "#/$defs/") {
			return nil, fmt.Errorf("external reference %q", ref)
		}
		name := strings.TrimPrefix(ref, "#/$defs/")
		def, ok := st.defs[name]
		if !ok {
			return nil, fmt.Errorf("missing definition for reference %q", ref)
		}
		return def, nil
	}
	if typ, _ := pm["type"].(string); typ == "array" {
		if items, ok := pm["items"].(map[string]any); ok {
			return objectSchemaForBuild(items, st)
		}
	}
	return pm, nil
}
