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
		return walkArgForNUL(a, 0, nil)
	}
	return validateBoundValue(val)
}

const maxValidateDepth = 8

func walkArgForNUL(a any, depth int, seen map[uintptr]bool) error {
	if a == nil || depth > maxValidateDepth {
		return nil
	}
	if named, ok := a.(sql.NamedArg); ok {
		return walkArgForNUL(named.Value, depth+1, seen)
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
	if !rv.IsValid() {
		return nil
	}
	switch rv.Kind() {
	case reflect.Ptr:
		if rv.IsNil() {
			return nil
		}
		ptr := rv.Pointer()
		if seen == nil {
			seen = make(map[uintptr]bool)
		}
		if seen[ptr] {
			return nil
		}
		seen[ptr] = true
		return walkArgForNUL(rv.Elem().Interface(), depth+1, seen)
	case reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return walkArgForNUL(rv.Elem().Interface(), depth+1, seen)
	case reflect.Slice, reflect.Array:
		elem := rv.Type().Elem()
		switch elem.Kind() {
		case reflect.String:
			for i := 0; i < rv.Len(); i++ {
				if err := ValidateText(rv.Index(i).String()); err != nil {
					return err
				}
			}
		case reflect.Interface, reflect.Slice, reflect.Array:
			for i := 0; i < rv.Len(); i++ {
				if err := walkArgForNUL(rv.Index(i).Interface(), depth+1, seen); err != nil {
					return err
				}
			}
		default:
			for i := 0; i < rv.Len(); i++ {
				if err := walkArgForNUL(rv.Index(i).Interface(), depth+1, seen); err != nil {
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
