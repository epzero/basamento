package expr

import (
	"fmt"
	"reflect"
	"sort"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"
)

const structTypeTraitMask = traits.FieldTesterType | traits.IndexerType

type fieldMeta struct {
	celType  *cel.Type
	required bool
}

type jsonObjectType struct {
	typeName string
	fields   map[string]fieldMeta
	adapter  ref.TypeAdapter //nolint:staticcheck // cel-go registry adapter type.
	objects  map[string]*jsonObjectType
}

func (t *jsonObjectType) ReflectType() reflect.Type { return nil }

func (t *jsonObjectType) TypeName() string { return t.typeName }

func (t *jsonObjectType) HasTrait(trait int) bool {
	return structTypeTraitMask&trait == trait
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

func objectData(target any) map[string]any {
	switch v := target.(type) {
	case *jsonObjectVal:
		return v.data
	case map[string]any:
		return v
	default:
		return nil
	}
}

func (t *jsonObjectType) FindFieldType(fieldName string) (*types.FieldType, bool) {
	meta, ok := t.fields[fieldName]
	if !ok {
		return nil, false
	}
	ft := &types.FieldType{
		Type: meta.celType,
		IsSet: func(target any) bool {
			data := objectData(target)
			if data == nil {
				return false
			}
			_, present := data[fieldName]
			return present
		},
		GetFrom: func(target any) (any, error) {
			data := objectData(target)
			if data == nil {
				return nil, fmt.Errorf("no such key")
			}
			v, present := data[fieldName]
			if !present {
				return nil, fmt.Errorf("no such key")
			}
			if obj := t.nestedObject(fieldName); obj != nil {
				if m, ok := v.(map[string]any); ok {
					return obj.Adapt(t.adapter, m), nil
				}
				if jv, ok := v.(*jsonObjectVal); ok {
					return jv, nil
				}
			}
			return v, nil
		},
	}
	return ft, true
}

func (t *jsonObjectType) nestedObject(fieldName string) *jsonObjectType {
	meta, ok := t.fields[fieldName]
	if !ok || meta.celType.Kind() != cel.StructKind {
		return nil
	}
	name := meta.celType.TypeName()
	if name == "" || t.objects == nil {
		return nil
	}
	return t.objects[name]
}

func (t *jsonObjectType) NewValue(adapter ref.TypeAdapter, fields map[string]ref.Val) ref.Val { //nolint:staticcheck
	data := make(map[string]any, len(fields))
	for k, v := range fields {
		n, err := v.ConvertToNative(reflect.TypeOf((*any)(nil)).Elem())
		if err == nil {
			data[k] = n
		}
	}
	return &jsonObjectVal{typ: t, adapter: adapter, data: data}
}

//nolint:staticcheck // cel-go registry still uses ref.TypeAdapter in method signatures.
func (t *jsonObjectType) Adapt(adapter ref.TypeAdapter, value any) ref.Val {
	switch v := value.(type) {
	case *jsonObjectVal:
		return v
	case map[string]any:
		data := make(map[string]any, len(v))
		for k, val := range v {
			if obj := t.nestedObject(k); obj != nil {
				if m, ok := val.(map[string]any); ok {
					data[k] = obj.Adapt(adapter, m)
					continue
				}
			}
			data[k] = val
		}
		return &jsonObjectVal{typ: t, adapter: adapter, data: data}
	default:
		return types.NewErr("type conversion error")
	}
}

type jsonObjectVal struct {
	typ     *jsonObjectType
	adapter ref.TypeAdapter //nolint:staticcheck
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
	if o.typ.TypeName() == typeVal.TypeName() {
		return o
	}
	return types.NewErr("type conversion error")
}

func (o *jsonObjectVal) Equal(other ref.Val) ref.Val {
	ov, ok := other.(*jsonObjectVal)
	if !ok {
		return types.Bool(false)
	}
	if o.typ.TypeName() != ov.typ.TypeName() {
		return types.Bool(false)
	}
	return types.Bool(reflect.DeepEqual(o.data, ov.data))
}

func (o *jsonObjectVal) HasTrait(trait int) bool {
	return structTypeTraitMask&trait == trait
}

func (o *jsonObjectVal) Get(index ref.Val) ref.Val {
	fieldName, ok := index.(types.String)
	if !ok {
		return types.MaybeNoSuchOverloadErr(index)
	}
	ft, found := o.typ.FindFieldType(string(fieldName))
	if !found {
		return types.NewErr("no such key")
	}
	if ft.GetFrom == nil {
		return types.NewErr("no such key")
	}
	fv, err := ft.GetFrom(o)
	if err != nil {
		return types.NewErrFromString(err.Error())
	}
	if rv, ok := fv.(ref.Val); ok {
		return rv
	}
	return o.adapter.NativeToValue(fv)
}

func (o *jsonObjectVal) IsSet(field ref.Val) ref.Val {
	fieldName, ok := field.(types.String)
	if !ok {
		return types.MaybeNoSuchOverloadErr(field)
	}
	ft, found := o.typ.FindFieldType(string(fieldName))
	if !found {
		return types.NewErr("no such key")
	}
	if ft.IsSet == nil {
		return types.Bool(false)
	}
	return types.Bool(ft.IsSet(o))
}

func (o *jsonObjectVal) Type() ref.Type {
	return o.typ
}

func (o *jsonObjectVal) Value() any {
	return o.data
}
