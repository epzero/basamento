package expr

import (
	"fmt"
	"reflect"
	"sort"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
)

type fieldMeta struct {
	celType  *cel.Type
	required bool
}

type jsonObjectType struct {
	typeName string
	fields   map[string]fieldMeta
	adapter  ref.TypeAdapter
}

func (t *jsonObjectType) ReflectType() reflect.Type { return nil }

func (t *jsonObjectType) TypeName() string { return t.typeName }

func (t *jsonObjectType) HasTrait(trait int) bool {
	return types.NewObjectType(t.typeName).HasTrait(trait)
}

func (t *jsonObjectType) objectType() *cel.Type {
	return cel.ObjectType(t.typeName)
}

func (t *jsonObjectType) FieldNames() []string {
	names := make([]string, 0, len(t.fields))
	for n := range t.fields {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (t *jsonObjectType) FindFieldType(fieldName string) (*types.FieldType, bool) {
	meta, ok := t.fields[fieldName]
	if !ok {
		return nil, false
	}
	ft := &types.FieldType{
		Type: meta.celType,
		IsSet: func(target any) bool {
			jv, ok := target.(*jsonObjectVal)
			if !ok {
				return false
			}
			_, present := jv.data[fieldName]
			return present
		},
		GetFrom: func(target any) (any, error) {
			jv, ok := target.(*jsonObjectVal)
			if !ok {
				return nil, fmt.Errorf("no such key")
			}
			v, present := jv.data[fieldName]
			if !present {
				return nil, fmt.Errorf("no such key")
			}
			return t.adapter.NativeToValue(v), nil
		},
	}
	return ft, true
}

func (t *jsonObjectType) NewValue(adapter ref.TypeAdapter, fields map[string]ref.Val) ref.Val {
	data := make(map[string]any, len(fields))
	for k, v := range fields {
		n, err := v.ConvertToNative(reflect.TypeOf((*any)(nil)).Elem())
		if err == nil {
			data[k] = n
		}
	}
	return &jsonObjectVal{typ: t, adapter: adapter, data: data}
}

func (t *jsonObjectType) Adapt(adapter ref.TypeAdapter, value any) ref.Val {
	switch v := value.(type) {
	case *jsonObjectVal:
		return v
	case map[string]any:
		return &jsonObjectVal{typ: t, adapter: adapter, data: v}
	default:
		return types.NewErr("type conversion error")
	}
}

type jsonObjectVal struct {
	typ     *jsonObjectType
	adapter ref.TypeAdapter
	data    map[string]any
}

func (o *jsonObjectVal) ConvertToNative(typeDesc reflect.Type) (any, error) {
	if typeDesc == reflect.TypeOf(map[string]any{}) || typeDesc.Kind() == reflect.Interface {
		return o.data, nil
	}
	return nil, fmt.Errorf("type conversion error")
}

func (o *jsonObjectVal) ConvertToType(typeVal ref.Type) ref.Val {
	if typeVal == types.TypeType {
		return types.NewTypeTypeWithParam(o.typ.objectType())
	}
	return types.NewErr("type conversion error")
}

func (o *jsonObjectVal) Equal(other ref.Val) ref.Val {
	return types.Bool(false)
}

func (o *jsonObjectVal) Type() ref.Type {
	return o.typ.objectType()
}

func (o *jsonObjectVal) Value() any {
	return o.data
}
