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
	celOpts := []cel.EnvOption{
		cel.CustomTypeAdapter(adapter),
		cel.CustomTypeProvider(reg),
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
			obj, err := e.objectFromSchema(st, adapter)
			if err != nil {
				return nil, err
			}
			if err := reg.RegisterType(obj); err != nil {
				return nil, fmt.Errorf("register object type %q: %w", st.typeName, err)
			}
			e.objects[st.typeName] = obj
		}
		celOpts = append(celOpts, cel.Variable(v.Name, st.celType))
	}
	celEnv, err := cel.NewEnv(celOpts...)
	if err != nil {
		return nil, err
	}
	e.celEnv = celEnv
	e.registry = reg
	return e, nil
}

func (e *Env) objectFromSchema(st *schemaType, adapter *sortedAdapter) (*jsonObjectType, error) {
	doc := st.schema
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
	for pname, pschema := range props {
		pm, ok := pschema.(map[string]any)
		if !ok {
			continue
		}
		visiting := map[string]bool{}
		if st.rootDef != "" {
			visiting[st.rootDef] = true
		}
		t, _, err := st.mapSchemaVisit(pm, st.typeName+"."+pname, visiting)
		if err != nil {
			return nil, err
		}
		fields[pname] = fieldMeta{celType: t, required: reqSet[pname]}
	}
	return &jsonObjectType{typeName: st.typeName, fields: fields, adapter: adapter}, nil
}
