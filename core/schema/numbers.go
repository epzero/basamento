package schema

import (
	"math"
	"math/big"
	"strconv"
	"strings"
)

// validateJSONNumberString rejects number literals whose exponent or mantissa digit
// count would be pathologically expensive in validation or canonicalization.
func validateJSONNumberString(s string) error {
	if n := numberMantissaDigitCount(s); n > maxNumberMantissaDigits {
		return errNumberBounds
	}
	if exp := numberExponentMagnitude(s); exp > maxNumberExponent {
		return errNumberBounds
	}
	if len(s) > maxNumberMantissaDigits+maxNumberExponent/10+20 {
		return errNumberBounds
	}
	return nil
}

func numberMantissaDigitCount(s string) int {
	if i := strings.LastIndexAny(s, "eE"); i >= 0 {
		s = s[:i]
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			n++
		}
	}
	return n
}

type numberBoundsError struct{}

func (numberBoundsError) Error() string { return "number out of bounds" }

var errNumberBounds = numberBoundsError{}

func numberExponentMagnitude(s string) int {
	i := strings.LastIndexAny(s, "eE")
	if i < 0 {
		return 0
	}
	expPart := s[i+1:]
	if expPart == "" {
		return maxNumberExponent + 1
	}
	sign := 1
	if expPart[0] == '+' || expPart[0] == '-' {
		if expPart[0] == '-' {
			sign = -1
		}
		expPart = expPart[1:]
	}
	if expPart == "" {
		return maxNumberExponent + 1
	}
	for _, c := range expPart {
		if c < '0' || c > '9' {
			return maxNumberExponent + 1
		}
	}
	n, err := strconv.Atoi(expPart)
	if err != nil {
		return maxNumberExponent + 1
	}
	if sign < 0 {
		n = -n
	}
	if n < 0 {
		n = -n
	}
	return n
}

// strictNumberSyntax reports whether text matches RFC 8259 number grammar (no extensions).
func strictNumberSyntax(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	i := 0
	if text[i] == '-' {
		i++
	}
	if i >= len(text) {
		return false
	}
	if text[i] == '+' {
		return false
	}
	if text[i] == '0' {
		i++
	} else if text[i] >= '1' && text[i] <= '9' {
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
	} else {
		return false
	}
	if i < len(text) && text[i] == '.' {
		i++
		if i >= len(text) || text[i] < '0' || text[i] > '9' {
			return false
		}
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
	}
	if i < len(text) && (text[i] == 'e' || text[i] == 'E') {
		i++
		if i < len(text) && (text[i] == '+' || text[i] == '-') {
			i++
		}
		if i >= len(text) || text[i] < '0' || text[i] > '9' {
			return false
		}
		for i < len(text) && text[i] >= '0' && text[i] <= '9' {
			i++
		}
	}
	return i == len(text)
}

// losesFloatPrecision reports whether text denotes a value different from the
// canonical form of f (REQ-SCHEMA-035). It compares exact rationals: a
// limited-precision big.Float would round long literals down to the canonical
// value and accept them silently.
func losesFloatPrecision(text string, f float64) bool {
	if !strictNumberSyntax(text) {
		return true
	}
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return true
	}
	canon := ecmaNumber(f)
	if text == canon {
		return false
	}
	exact, ok := new(big.Rat).SetString(text)
	if !ok {
		return true
	}
	canonical, ok := new(big.Rat).SetString(canon)
	if !ok {
		return true
	}
	return exact.Cmp(canonical) != 0
}

// ecmaNumber formats f as ECMAScript Number::toString (RFC 8785 shortest form).
func ecmaNumber(f float64) string {
	if math.Signbit(f) && f == 0 {
		f = 0
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	abs := math.Abs(f)
	if abs > 0 && (abs < 1e-6 || abs >= 1e21) {
		return ecmaExponential(f)
	}
	fixed := ecmaFixed(f)
	if roundTrips(f, fixed) {
		if p := powerOfTenExponent(f); p >= 20 {
			exp := ecmaExponential(f)
			if roundTrips(f, exp) && len(exp) < len(fixed) {
				return exp
			}
		}
		return fixed
	}
	return ecmaExponential(f)
}

// powerOfTenExponent reports n when f = ±10^n, else -1.
func powerOfTenExponent(f float64) int {
	if f == 0 {
		return -1
	}
	abs := math.Abs(f)
	if math.Abs(abs-math.Trunc(abs)) > 0 {
		return -1
	}
	n := math.Log10(abs)
	if math.Abs(n-math.Trunc(n)) > 1e-9 {
		return -1
	}
	return int(math.Round(n))
}

func ecmaFixed(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		return "0"
	}
	return s
}

func ecmaExponential(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if strings.ContainsAny(s, "eE") {
		s = strings.Replace(s, "E", "e", 1)
		parts := strings.SplitN(s, "e", 2)
		exp, _ := strconv.Atoi(parts[1])
		if exp > 0 {
			s = parts[0] + "e+" + strconv.Itoa(exp)
		} else {
			s = parts[0] + "e" + strconv.Itoa(exp)
		}
	}
	return s
}

func roundTrips(f float64, text string) bool {
	g, err := strconv.ParseFloat(text, 64)
	return err == nil && g == f
}
