package schema

// Untrusted JSON limits (conservative; not fixed in the spec).
//
// Validation accepts JSON Schema numbers beyond IEEE doubles, but rejects literals
// whose exponent or mantissa digit count would make hash/compare work quadratic in
// hostile small payloads (see validateJSONNumberString).
const (
	maxNestingDepth         = 10000
	maxNumberExponent       = 8192 // inclusive; allows 1e308, blocks 1e900000-class attacks
	maxNumberMantissaDigits = 1000 // digit count before e/E; fits uint64 integers and long decimals
)
