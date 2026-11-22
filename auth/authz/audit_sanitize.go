package authz

import (
	"unicode"
	"unicode/utf8"
)

const maxAuditFieldBytes = 256

func sanitizeAuditPart(s string) string {
	if !validAuditField(s) {
		return "[invalid]"
	}
	return s
}

func sanitizeAuditResource(kind, id string) string {
	return sanitizeAuditPart(kind) + ":" + sanitizeAuditPart(id)
}

func validAuditField(s string) bool {
	if s == "" {
		return true
	}
	if !utf8.ValidString(s) || len(s) > maxAuditFieldBytes {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Zl, r) || unicode.Is(unicode.Zp, r) {
			return false
		}
	}
	return true
}
