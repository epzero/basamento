package blob

import "math"

// readLimit is the LimitReader bound for a staged object of at most max bytes.
func readLimit(max int64) int64 {
	if max == math.MaxInt64 {
		return math.MaxInt64
	}
	return max + 1
}
