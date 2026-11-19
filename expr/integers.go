package expr

import "math"

// float64IntegralInt64 reports whether f is an integral value that fits in int64.
// When true, i is that value.
func float64IntegralInt64(f float64) (i int64, ok bool) {
	if f != math.Trunc(f) {
		return 0, false
	}
	const maxInt64Exclusive = 9223372036854775808.0
	const minInt64Inclusive = -9223372036854775808.0
	if f >= maxInt64Exclusive || f < minInt64Inclusive {
		return 0, false
	}
	return int64(f), true
}

// float64IntegralOutsideInt64 reports whether f is integral but outside the int64 range.
func float64IntegralOutsideInt64(f float64) bool {
	if f != math.Trunc(f) {
		return false
	}
	const maxInt64Exclusive = 9223372036854775808.0
	const minInt64Inclusive = -9223372036854775808.0
	return f >= maxInt64Exclusive || f < minInt64Inclusive
}
