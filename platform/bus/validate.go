package bus

import (
	"strings"
	"unicode"
)

// DefaultMaxMessageSize is the maximum publish payload size (REQ-BUS-011).
const DefaultMaxMessageSize = 1 << 20

// DefaultSubBuffer is the undelivered message buffer per subscription (REQ-BUS-010).
const DefaultSubBuffer = 256

// MaxKVKeyLen is the longest KV key the client accepts before calling JetStream.
const MaxKVKeyLen = 512

// MaxKVValueBytes is the largest logical KV value for Put and CompareAndSwap.
func MaxKVValueBytes(maxMessage int) int {
	return maxMessage - deadlineSize
}

// ValidSubject reports whether subject matches the Bus grammar in platform/platform.go.
func ValidSubject(subject string) bool {
	if subject == "" {
		return false
	}
	if strings.ContainsAny(subject, " \t\n\r") {
		return false
	}
	tokens := strings.Split(subject, ".")
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

func validKeyRune(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9':
		return true
	case b == '-', b == '_', b == '/', b == '=', b == '.':
		return true
	default:
		return false
	}
}

// ValidKey reports whether key matches the NATS KV grammar enforced by this package.
func ValidKey(key string) bool {
	if key == "" || len(key) > MaxKVKeyLen {
		return false
	}
	if strings.HasPrefix(key, ".") || strings.HasSuffix(key, ".") {
		return false
	}
	if strings.ContainsAny(key, " \t\n\r") || strings.Contains(key, "*") || strings.Contains(key, ">") {
		return false
	}
	for i := 0; i < len(key); i++ {
		if !validKeyRune(key[i]) {
			return false
		}
	}
	for _, tok := range strings.Split(key, ".") {
		if tok == "" {
			return false
		}
	}
	return true
}

func checkKVValueLen(value []byte, maxMessage int) error {
	if len(value) > MaxKVValueBytes(maxMessage) {
		return valueTooLarge()
	}
	return nil
}
