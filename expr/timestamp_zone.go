package expr

import (
	"errors"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/overloads"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
)

var errTimezoneLocal = errors.New(`timezone "Local" is not allowed`)

func timezoneDisallowed(tz ref.Val) ref.Val {
	if s, ok := tz.(types.String); ok && string(s) == "Local" {
		return types.WrapErr(errTimezoneLocal)
	}
	return nil
}

func timestampReceive(ts types.Timestamp, function, overload string, args []ref.Val) ref.Val {
	if len(args) == 1 {
		if bad := timezoneDisallowed(args[0]); bad != nil {
			return bad
		}
	}
	return ts.Receive(function, overload, args)
}

type deterministicTimestampLib struct{}

func (deterministicTimestampLib) CompileOptions() []cel.EnvOption {
	var opts []cel.EnvOption
	withTz := func(function, overloadID string) {
		opts = append(opts, cel.Function(function,
			cel.MemberOverload(overloadID, []*cel.Type{cel.TimestampType, cel.StringType}, cel.IntType,
				cel.BinaryBinding(func(lhs, rhs ref.Val) ref.Val {
					if bad := timezoneDisallowed(rhs); bad != nil {
						return bad
					}
					t, ok := lhs.(types.Timestamp)
					if !ok {
						return types.MaybeNoSuchOverloadErr(lhs)
					}
					return timestampReceive(t, function, overloadID, []ref.Val{rhs})
				})),
		))
	}
	withTz(overloads.TimeGetFullYear, overloads.TimestampToYearWithTz)
	withTz(overloads.TimeGetMonth, overloads.TimestampToMonthWithTz)
	withTz(overloads.TimeGetDayOfYear, overloads.TimestampToDayOfYearWithTz)
	withTz(overloads.TimeGetDayOfMonth, overloads.TimestampToDayOfMonthZeroBasedWithTz)
	withTz(overloads.TimeGetDate, overloads.TimestampToDayOfMonthOneBasedWithTz)
	withTz(overloads.TimeGetDayOfWeek, overloads.TimestampToDayOfWeekWithTz)
	withTz(overloads.TimeGetHours, overloads.TimestampToHoursWithTz)
	withTz(overloads.TimeGetMinutes, overloads.TimestampToMinutesWithTz)
	withTz(overloads.TimeGetSeconds, overloads.TimestampToSecondsWithTz)
	withTz(overloads.TimeGetMilliseconds, overloads.TimestampToMillisecondsWithTz)
	return opts
}

func (deterministicTimestampLib) ProgramOptions() []cel.ProgramOption { return nil }
