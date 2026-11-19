package expr

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/itchyny/gojq"
)

// scanBannedAST rejects jq programs that call or bind banned impure builtins.
// Function calls (Func.Name), definitions and parameters (FuncDef), pattern
// variables whose names are reserved (Pattern.Name), label/break names, import
// forms, and object shorthand keys that name $ENV-style variables (ObjectKeyVal
// keys beginning with $) are blocked. Field selectors (.now), object literal
// keys ({now: .now}, {now}), and pattern keys without a $ prefix are data only
// and are allowed.
//
// gojqASTStructs is every struct type that may appear in a parsed query AST.
// The reflection walker fail-closes on any other gojq struct.
var gojqASTStructs = map[string]bool{
	"Query": true, "Import": true, "FuncDef": true, "Term": true,
	"Unary": true, "Pattern": true, "PatternObject": true, "Index": true,
	"Func": true, "String": true, "Object": true, "ObjectKeyVal": true,
	"Array": true, "Suffix": true, "If": true, "IfElif": true,
	"Try": true, "Reduce": true, "Foreach": true, "Label": true,
	"ConstTerm": true, "ConstObject": true, "ConstObjectKeyVal": true, "ConstArray": true,
}

func scanBannedAST(root *gojq.Query) (string, bool) {
	if root == nil {
		return "", false
	}
	name, blocked, err := walkAST(reflect.ValueOf(root))
	if err != nil {
		return "unsupported construct", true
	}
	return name, blocked
}

func walkAST(v reflect.Value) (string, bool, error) {
	if !v.IsValid() {
		return "", false, nil
	}
	for v.Kind() == reflect.Interface {
		if v.IsNil() {
			return "", false, nil
		}
		v = v.Elem()
	}
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return "", false, nil
		}
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.Struct:
		if pkg := v.Type().PkgPath(); pkg == "github.com/itchyny/gojq" {
			if v.Type().Name() == "Func" {
				if n, ok := checkBannedName(v.FieldByName("Name").String()); ok {
					return n, true, nil
				}
			}
			if v.Type().Name() == "Query" {
				imports := v.FieldByName("Imports")
				if imports.IsValid() && imports.Len() > 0 {
					return "import", true, nil
				}
			}
			if v.Type().Name() == "Term" {
				tt := v.FieldByName("Type").Int()
				if tt < int64(gojq.TermTypeIdentity) || tt > int64(gojq.TermTypeQuery) {
					return fmt.Sprintf("unsupported gojq term type %d", tt), true, nil
				}
				if n, ok := checkBannedName(v.FieldByName("Break").String()); ok {
					return n, true, nil
				}
			}
			switch v.Type().Name() {
			case "Pattern":
				if n, ok := checkBannedName(v.FieldByName("Name").String()); ok {
					return n, true, nil
				}
			case "FuncDef":
				if n, ok := checkBannedName(v.FieldByName("Name").String()); ok {
					return n, true, nil
				}
				args := v.FieldByName("Args")
				for i := 0; i < args.Len(); i++ {
					if n, ok := checkBannedName(args.Index(i).String()); ok {
						return n, true, nil
					}
				}
			case "ObjectKeyVal":
				if n, ok := checkBannedVarKey(v.FieldByName("Key").String()); ok {
					return n, true, nil
				}
			}
			if v.Type().Kind() == reflect.Struct && !gojqASTStructs[v.Type().Name()] {
				switch v.Type().Name() {
				case "Operator", "TermType":
					// named integer types
				default:
					return "", true, fmt.Errorf("unhandled gojq AST type %q", v.Type().Name())
				}
			}
		}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.PkgPath != "" && !f.IsExported() {
				continue
			}
			n, blocked, err := walkAST(v.Field(i))
			if err != nil || blocked {
				return n, blocked, err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			n, blocked, err := walkAST(v.Index(i))
			if err != nil || blocked {
				return n, blocked, err
			}
		}
	case reflect.Map:
		return "", true, fmt.Errorf("unexpected map in gojq AST")
	case reflect.String, reflect.Int, reflect.Int64, reflect.Bool, reflect.Uint, reflect.Uint64, reflect.Float64:
		return "", false, nil
	default:
		if v.Type().PkgPath() == "github.com/itchyny/gojq" {
			return "", true, fmt.Errorf("unhandled gojq kind %s in %s", v.Kind(), v.Type())
		}
	}
	return "", false, nil
}

func checkBannedName(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	if bannedNames[name] || outputLimitReservedName(name) {
		return strings.TrimPrefix(name, "$"), true
	}
	return "", false
}

// checkBannedVarKey rejects {$ENV} and other banned identifiers used as $-prefixed
// object field shorthand (jq binds them to special variables, not plain data keys).
func checkBannedVarKey(key string) (string, bool) {
	if key == "" || key[0] != '$' {
		return "", false
	}
	return checkBannedName(key)
}
