package sql

import (
	"database/sql"
	"database/sql/driver"
	"reflect"
)

func validateArgs(args []any) error {
	for _, a := range args {
		if err := validateOneArg(a); err != nil {
			return err
		}
	}
	return nil
}

func validateOneArg(a any) error {
	if a == nil {
		return nil
	}
	if named, ok := a.(sql.NamedArg); ok {
		return validateOneArg(named.Value)
	}
	val, err := driver.DefaultParameterConverter.ConvertValue(a)
	if err != nil {
		return walkArgForNUL(a, 0)
	}
	return validateBoundValue(val)
}

const maxValidateDepth = 8

func walkArgForNUL(a any, depth int) error {
	if a == nil || depth > maxValidateDepth {
		return nil
	}
	if named, ok := a.(sql.NamedArg); ok {
		return walkArgForNUL(named.Value, depth+1)
	}
	switch v := a.(type) {
	case string:
		return ValidateText(v)
	case []string:
		for _, s := range v {
			if err := ValidateText(s); err != nil {
				return err
			}
		}
		return nil
	case []byte:
		return nil
	}
	rv := reflect.ValueOf(a)
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		if rv.Type().Elem().Kind() == reflect.String {
			for i := 0; i < rv.Len(); i++ {
				if err := ValidateText(rv.Index(i).String()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateBoundValue(val any) error {
	if val == nil {
		return nil
	}
	switch v := val.(type) {
	case string:
		return ValidateText(v)
	case []byte:
		return nil
	default:
		return nil
	}
}
