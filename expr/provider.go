package expr

import (
	"reflect"

	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// sortedTypeProvider wraps *types.Registry for CEL type resolution without
// exposing Registry.NativeToValue, so ext.Encoders (json.parse/yaml.parse)
// keep using the sorted map adapter.
type sortedTypeProvider struct {
	reg *types.Registry
}

func newSortedTypeProvider(reg *types.Registry) *sortedTypeProvider {
	return &sortedTypeProvider{reg: reg}
}

func (p *sortedTypeProvider) RegisterType(types ...ref.Type) error {
	return p.reg.RegisterType(types...)
}

func (p *sortedTypeProvider) RegisterDescriptor(fileDesc protoreflect.FileDescriptor) error {
	return p.reg.RegisterDescriptor(fileDesc)
}

func (p *sortedTypeProvider) RegisterNativeType(refType reflect.Type, opts ...types.NativeTypeOption) error {
	return p.reg.RegisterNativeType(refType, opts...)
}

func (p *sortedTypeProvider) RegisterNativeTypeDesc(desc *types.NativeTypeDesc) error {
	return p.reg.RegisterNativeTypeDesc(desc)
}

func (p *sortedTypeProvider) RegisterMessage(message proto.Message) error {
	return p.reg.RegisterMessage(message)
}

func (p *sortedTypeProvider) EnumValue(enumName string) ref.Val {
	return p.reg.EnumValue(enumName)
}

func (p *sortedTypeProvider) FindIdent(identName string) (ref.Val, bool) {
	return p.reg.FindIdent(identName)
}

func (p *sortedTypeProvider) FindStructType(structType string) (*types.Type, bool) {
	return p.reg.FindStructType(structType)
}

func (p *sortedTypeProvider) FindStructFieldNames(structType string) ([]string, bool) {
	return p.reg.FindStructFieldNames(structType)
}

func (p *sortedTypeProvider) FindStructFieldType(structType, fieldName string) (*types.FieldType, bool) {
	return p.reg.FindStructFieldType(structType, fieldName)
}

func (p *sortedTypeProvider) NewValue(structType string, fields map[string]ref.Val) ref.Val {
	return p.reg.NewValue(structType, fields)
}