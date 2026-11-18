package sql

import (
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
	val, err := driver.DefaultParameterConverter.ConvertValue(a)
	if err != nil {
		return nil
	}
	return validateBoundValue(val)
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
