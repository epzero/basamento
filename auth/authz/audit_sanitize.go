package authz

import "unicode/utf8"

const maxAuditFieldBytes = 256

func sanitizeAuditPart(s string) string {
	if !validAuditField(s) {
		return "[invalid]"
	}
	if len(s) > maxAuditFieldBytes {
		return truncateUTF8(s, maxAuditFieldBytes)
	}
	return s
}

func sanitizeAuditResource(kind, id string) string {
	return sanitizeAuditPart(kind) + ":" + sanitizeAuditPart(id)
}

func validAuditField(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r <= 0x1f || r == 0x7f {
			return false
		}
	}
	return true
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	if maxBytes == 0 {
		return "[invalid]"
	}
	return s[:maxBytes]
}
