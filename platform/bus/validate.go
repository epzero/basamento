package bus

import (
	"strings"
	"unicode"
)

// DefaultMaxMessageSize is the maximum publish payload size (REQ-BUS-011).
const DefaultMaxMessageSize = 1 << 20

// DefaultSubBuffer is the undelivered message buffer per subscription (REQ-BUS-010).
const DefaultSubBuffer = 256

// ValidSubject reports whether subject matches the Bus grammar in platform/platform.go.
func ValidSubject(subject string) bool {
	if subject == "" {
		return false
	}
	if strings.ContainsAny(subject, " \t\n\r") {
		return false
	}
	tokens := strings.Split(subject, ".")
	if len(tokens) == 0 {
		return false
	}
	for _, tok := range tokens {
		if tok == "" || tok == "*" || tok == ">" {
			return false
		}
		for _, r := range tok {
			if !validSubjectRune(r) {
				return false
			}
		}
	}
	return true
}

func validSubjectRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_'
}

// ValidKey reports whether key matches the KV grammar in platform/platform.go.
func ValidKey(key string) bool {
	if key == "" || strings.HasPrefix(key, ".") || strings.HasSuffix(key, ".") {
		return false
	}
	if strings.ContainsAny(key, " \t\n\r") {
		return false
	}
	for _, r := range key {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
		case r == '-', r == '_', r == '/', r == '=', r == '.':
		default:
			return false
		}
	}
	return !strings.Contains(key, "*") && !strings.Contains(key, ">")
}
