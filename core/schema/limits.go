package schema

// Untrusted JSON limits (conservative; not fixed in the spec).
const (
	maxNestingDepth       = 10000
	maxNumberExponent     = 1000000 // math/big Rat.SetString rejects |exp| above 1e6
	maxNumberDigitRun     = 10000
)