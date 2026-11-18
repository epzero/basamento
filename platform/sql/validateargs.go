package sql

import (
	"database/sql"
	"database/sql/driver"
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
	if v, ok := a.(driver.Valuer); ok {
		val, err := v.Value()
		if err != nil {
			return nil
		}
		return validateBoundValue(val)
	}
	val, err := driver.DefaultParameterConverter.ConvertValue(a)
	if err != nil {
		return nil
	}
	return validateBoundValue(val)
}

func validateBoundValue(val any) error {
	switch v := val.(type) {
	case string:
		return ValidateText(v)
	case []byte:
		return nil
	default:
		if s, ok := stringBindValue(val); ok {
			return ValidateText(s)
		}
		return nil
	}
}

func stringBindValue(val any) (string, bool) {
	switch v := val.(type) {
	case *string:
		if v == nil {
			return "", false
		}
		return *v, true
	case sql.NullString:
		if !v.Valid {
			return "", false
		}
		return v.String, true
	default:
		return "", false
	}
}
